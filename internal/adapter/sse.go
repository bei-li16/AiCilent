package adapter

import (
	"io"
	"time"

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
	usage   *stream.UsageMeter
}

func newEventScanner(src io.Reader, format string, meters ...*stream.UsageMeter) *eventScanner {
	scanner := &eventScanner{decoder: stream.NewDecoder(src), state: stream.State{Format: format}}
	if len(meters) > 0 {
		scanner.usage = meters[0]
	}
	return scanner
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
		if s.usage != nil {
			s.usage.ObserveEvent(e, s.state.Format, time.Now())
		}
		s.event = e
		s.done = s.state.Terminated
		return true
	}
}

func (s *eventScanner) Text() string { return "data: " + string(s.event.Data) }
func (s *eventScanner) Err() error   { return s.err }
