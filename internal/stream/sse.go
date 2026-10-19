// Package stream handles SSE framing and protocol completion independently of
// HTTP reads and writes. A data line is not an event until its blank line arrives.
package stream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const MaxEventBytes = 1 << 20

type Event struct {
	Raw     []byte
	Type    string
	Data    []byte
	HasData bool
}

// Framer retains at most one incomplete event between calls. scan and lineStart
// prevent repeatedly scanning a large event when the transport splits it up.
type Framer struct {
	pending   []byte
	scan      int
	lineStart int
}

func (f *Framer) Append(p []byte) { f.pending = append(f.pending, p...) }
func (f *Framer) Buffered() int   { return len(f.pending) }

func (f *Framer) Next(final bool) (Event, bool, error) {
	for f.scan < len(f.pending) {
		i := f.scan
		if i >= MaxEventBytes {
			return Event{}, false, fmt.Errorf("SSE event exceeds %d bytes", MaxEventBytes)
		}
		c := f.pending[i]
		if c != '\r' && c != '\n' {
			f.scan++
			continue
		}
		if c == '\r' && i+1 == len(f.pending) && !final && i != f.lineStart {
			break // a CRLF pair can straddle two reads
		}
		end := i + 1
		if c == '\r' && end < len(f.pending) && f.pending[end] == '\n' {
			end++
		}
		blank := i == f.lineStart
		f.scan, f.lineStart = end, end
		if !blank {
			continue
		}
		raw := bytes.Clone(f.pending[:end])
		f.pending = f.pending[end:]
		f.scan, f.lineStart = 0, 0
		return parseEvent(raw), true, nil
	}
	if len(f.pending) > MaxEventBytes {
		return Event{}, false, fmt.Errorf("SSE event exceeds %d bytes", MaxEventBytes)
	}
	return Event{}, false, nil
}

func parseEvent(raw []byte) Event {
	e := Event{Raw: raw}
	text := strings.ReplaceAll(strings.TrimPrefix(string(raw), "\ufeff"), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var data []string
	for _, line := range strings.Split(text, "\n") {
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			e.Type = value
		case "data":
			e.HasData = true
			data = append(data, value)
		}
	}
	e.Data = []byte(strings.Join(data, "\n"))
	return e
}

type Decoder struct {
	r       io.Reader
	framer  Framer
	readErr error
	buf     [4096]byte
}

func NewDecoder(r io.Reader) *Decoder { return &Decoder{r: r} }

func (d *Decoder) Next() (Event, error) {
	for emptyReads := 0; ; {
		e, ok, err := d.framer.Next(d.readErr != nil)
		if err != nil || ok {
			return e, err
		}
		if d.readErr != nil {
			if d.readErr == io.EOF && d.framer.Buffered() != 0 {
				return Event{}, io.ErrUnexpectedEOF
			}
			return Event{}, d.readErr
		}
		n, err := d.r.Read(d.buf[:])
		d.framer.Append(d.buf[:n])
		d.readErr = err
		if n == 0 && err == nil {
			emptyReads++
			if emptyReads >= 100 {
				return Event{}, io.ErrNoProgress
			}
		}
	}
}

type Failure struct{ Message string }

func (e *Failure) Error() string { return "upstream SSE error: " + e.Message }

// State distinguishes a semantic finish (which may precede usage) from the
// protocol terminator. EOF compatibility is allowed only after semantic finish;
// a timeout or cancellation never becomes successful merely because of it.
type State struct {
	Format     string
	Answered   bool
	Terminated bool
	choices    map[int]bool
}

func (s State) Clone() State {
	if s.choices != nil {
		original := s.choices
		s.choices = make(map[int]bool, len(s.choices))
		for k, v := range original {
			s.choices[k] = v
		}
	}
	return s
}

func (s *State) Observe(e Event) error {
	if !e.HasData {
		return nil
	}
	data := bytes.TrimSpace(e.Data)
	if bytes.Equal(data, []byte("[DONE]")) && (s.Format == "" || s.Format == "openai") {
		s.Terminated, s.Answered = true, true
		return nil
	}
	var payload struct {
		Type    string          `json:"type"`
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Index        int     `json:"index"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Delta json.RawMessage `json:"delta"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("invalid SSE JSON: %w", err)
	}
	if (len(payload.Error) > 0 && string(payload.Error) != "null") || e.Type == "error" || payload.Type == "error" || payload.Type == "response.failed" {
		msg := string(data)
		if len(msg) > 500 {
			msg = msg[:500] + "..."
		}
		return &Failure{Message: msg}
	}
	if (s.Format == "" || s.Format == "anthropic") && payload.Type == "message_stop" ||
		(s.Format == "" || s.Format == "responses") && (payload.Type == "response.completed" || payload.Type == "response.incomplete") {
		s.Terminated, s.Answered = true, true
		return nil
	}
	if (s.Format == "" || s.Format == "anthropic") && payload.Type == "message_delta" {
		var delta struct {
			StopReason *string `json:"stop_reason"`
		}
		if err := json.Unmarshal(payload.Delta, &delta); err != nil {
			return fmt.Errorf("invalid message_delta: %w", err)
		}
		if delta.StopReason != nil && *delta.StopReason != "" {
			s.Answered = true
		}
	}
	if s.Format == "" || s.Format == "openai" {
		for _, c := range payload.Choices {
			if s.choices == nil {
				s.choices = make(map[int]bool)
			}
			if c.FinishReason != nil && *c.FinishReason != "" {
				switch *c.FinishReason {
				case "error", "network_error", "insufficient_system_resource":
					return &Failure{Message: "finish_reason=" + *c.FinishReason}
				}
				s.choices[c.Index] = true
			} else if _, exists := s.choices[c.Index]; !exists {
				s.choices[c.Index] = false
			}
		}
		if len(s.choices) > 0 {
			s.Answered = true
			for _, finished := range s.choices {
				s.Answered = s.Answered && finished
			}
		}
	}
	return nil
}

func (s *State) End() error {
	if s.Terminated || s.Answered {
		return nil
	}
	return io.ErrUnexpectedEOF
}
