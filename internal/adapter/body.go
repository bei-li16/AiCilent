package adapter

import (
	"fmt"
	"io"
)

// Successful JSON can contain multimodal data; errors only need a diagnostic prefix.
const MaxResponseBodyBytes = 50 << 20
const MaxErrorBodyBytes = 64 << 10

func ReadResponseBody(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxResponseBodyBytes+1))
	if err == nil && len(b) > MaxResponseBodyBytes {
		return nil, fmt.Errorf("upstream response exceeds %d bytes", MaxResponseBodyBytes)
	}
	return b, err
}

func ReadErrorBody(r io.Reader) string {
	b, err := io.ReadAll(io.LimitReader(r, MaxErrorBodyBytes+1))
	if len(b) > MaxErrorBodyBytes {
		return string(b[:MaxErrorBodyBytes]) + " [truncated]"
	}
	if err != nil {
		return string(b) + " [read error: " + err.Error() + "]"
	}
	return string(b)
}
