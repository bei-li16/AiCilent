package router

import (
	"errors"
	"fmt"
	"io"

	"ai-proxy/internal/stream"
)

var (
	errStreamTerminal       = errors.New("stream terminal event forwarded")
	errClientGoneIncomplete = errors.New("client disconnected before stream completion")
)

type downstreamWriteError struct{ err error }

func (e *downstreamWriteError) Error() string { return "write downstream: " + e.err.Error() }
func (e *downstreamWriteError) Unwrap() error { return e.err }

type localCapacityError struct{ err error }

func (e *localCapacityError) Error() string {
	return "wait for upstream connection slot: " + e.err.Error()
}
func (e *localCapacityError) Unwrap() error { return e.err }

// terminalWriter forwards complete events, then records what was successfully
// written. Neither a partial data line nor a failed write establishes completion.
type terminalWriter struct {
	inner    io.Writer
	framer   stream.Framer
	progress stream.State
}

func newTerminalWriter(inner io.Writer, format ...string) *terminalWriter {
	w := &terminalWriter{inner: inner}
	if len(format) > 0 {
		w.progress.Format = format[0]
	}
	return w
}

func (w *terminalWriter) Answered() bool   { return w.progress.Answered }
func (w *terminalWriter) Terminated() bool { return w.progress.Terminated }

func (w *terminalWriter) Write(p []byte) (int, error) {
	w.framer.Append(p)
	err := w.forwardEvents(false)
	return len(p), err
}

func (w *terminalWriter) forwardEvents(final bool) error {
	for {
		event, ok, err := w.framer.Next(final)
		if err != nil || !ok {
			return err
		}
		next := w.progress.Clone()
		if err := next.Observe(event); err != nil {
			return err
		}
		n, err := w.inner.Write(event.Raw)
		if err == nil && n != len(event.Raw) {
			err = io.ErrShortWrite
		}
		if err != nil {
			return &downstreamWriteError{err: err}
		}
		w.progress = next
		if w.Terminated() {
			return errStreamTerminal
		}
	}
}

func (w *terminalWriter) Finish() error {
	if err := w.forwardEvents(true); err != nil {
		return err
	}
	if w.framer.Buffered() != 0 {
		return io.ErrUnexpectedEOF
	}
	return w.progress.End()
}

func writeStreamTerminator(w io.Writer, format string) error {
	if format == "anthropic" {
		_, err := fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		return err
	}
	if format == "openai" {
		_, err := fmt.Fprint(w, "data: [DONE]\n\n")
		return err
	}
	return io.ErrUnexpectedEOF // Responses completion needs its full response object.
}
