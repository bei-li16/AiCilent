package adapter

import (
	"io"

	"ai-proxy/internal/stream"
)

// eventScanner presents complete SSE data events to the converters. It never
// reads again after a terminator and rejects EOF without a semantic finish.
type eventScanner struct {
	decoder *stream.Decoder
	state   stream.State
	event   stream.Event
	err     error
	done    bool
}

func newEventScanner(src io.Reader, format string) *eventScanner {
	return &eventScanner{decoder: stream.NewDecoder(src), state: stream.State{Format: format}}
}

func (s *eventScanner) Scan() bool {
	if s.done {
		return false
	}
	for {
		e, err := s.decoder.Next()
		if err != nil {
			s.done = true
			if err == io.EOF {
				err = s.state.End()
			}
			s.err = err
			return false
		}
		if !e.HasData {
			continue
		}
		if err := s.state.Observe(e); err != nil {
			s.err, s.done = err, true
			return false
		}
		s.event = e
		s.done = s.state.Terminated
		return true
	}
}

func (s *eventScanner) Text() string { return "data: " + string(s.event.Data) }
func (s *eventScanner) Err() error   { return s.err }
