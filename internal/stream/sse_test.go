package stream

import (
	"errors"
	"io"
	"strings"
	"testing"
)

type byteReader struct{ text string }

func (r *byteReader) Read(p []byte) (int, error) {
	if r.text == "" {
		return 0, io.EOF
	}
	p[0], r.text = r.text[0], r.text[1:]
	return 1, nil
}

func TestDecoderPreservesCompleteEventsAcrossByteBoundaries(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		raw := strings.Join([]string{"event: message_stop", "data: {", "data: \"type\": \"message_stop\"}", "", ""}, newline)
		d := NewDecoder(&byteReader{text: raw})
		e, err := d.Next()
		if err != nil {
			t.Fatal(err)
		}
		// A blank CR dispatches immediately. A following optional LF may not
		// have arrived yet when the transport delivers one byte at a time.
		if !strings.HasPrefix(raw, string(e.Raw)) || e.Type != "message_stop" {
			t.Fatalf("event=%#v", e)
		}
		s := State{Format: "anthropic"}
		if err := s.Observe(e); err != nil || !s.Terminated {
			t.Fatalf("state=%+v err=%v", s, err)
		}
		for {
			tail, err := d.Next()
			if err == io.EOF {
				break
			}
			if err != nil || tail.HasData {
				t.Fatalf("end=%v tail=%+v", err, tail)
			}
		}
	}
}

func TestDecoderRejectsIncompleteAndOversizedEvents(t *testing.T) {
	for _, raw := range []string{"data: [DONE]\n", "event: message_stop\n", "data: [DONE]"} {
		if _, err := NewDecoder(strings.NewReader(raw)).Next(); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("%q: %v", raw, err)
		}
	}
	if _, err := NewDecoder(strings.NewReader("data: " + strings.Repeat("x", MaxEventBytes))).Next(); err == nil {
		t.Fatal("oversized event accepted")
	}
}

func TestStateUsesStructuralMarkersAndAllChoices(t *testing.T) {
	s := State{Format: "openai"}
	for _, raw := range []string{
		`{"metadata":{"finish_reason":"stop","type":"message_stop"},"choices":[{"index":0,"finish_reason":null},{"index":1,"finish_reason":null}]}`,
		`{"choices":[{"index":0,"finish_reason": "stop"}]}`,
	} {
		if err := s.Observe(Event{HasData: true, Data: []byte(raw)}); err != nil {
			t.Fatal(err)
		}
		if s.Answered || s.Terminated {
			t.Fatalf("premature finish: %+v", s)
		}
	}
	copy := s.Clone()
	if err := copy.Observe(Event{HasData: true, Data: []byte(`{"choices":[{"index":1,"finish_reason":"tool_calls"}]}`)}); err != nil {
		t.Fatal(err)
	}
	if !copy.Answered || s.Answered {
		t.Fatal("choice state or clone isolation failed")
	}
	if err := s.End(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("end=%v", err)
	}
}

func TestErrorCannotBeTreatedAsCompletion(t *testing.T) {
	for _, raw := range []string{`{"error":{"message":"busy"}}`, `{"type":"response.failed","response":{"error":{"message":"failed"}}}`, `{"choices":[{"finish_reason":"network_error"}]}`} {
		s := State{}
		var failure *Failure
		if err := s.Observe(Event{HasData: true, Data: []byte(raw)}); !errors.As(err, &failure) || s.Terminated {
			t.Fatalf("%s: state=%+v error=%v", raw, s, err)
		}
	}
}
