package adapter

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestConversionRejectsTruncationAndInBandErrors(t *testing.T) {
	partial := "data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"half\"}}]}\n\n"
	for _, format := range []string{"anthropic", "responses"} {
		for _, tail := range []string{"", "data: {\"error\":{\"message\":\"busy\"}}\n\n", "data: [DONE]\n"} {
			var output strings.Builder
			err := StreamConvertResponse(strings.NewReader(partial+tail), &output, "openai", format)
			if err == nil {
				t.Fatalf("%s tail %q accepted", format, tail)
			}
			if strings.Contains(output.String(), "response.completed") || strings.Contains(output.String(), "message_stop") {
				t.Fatalf("false completion: %s", output.String())
			}
		}
	}
}

func TestConversionAcceptsNoSpaceMultilineAndCleanFinishedEOF(t *testing.T) {
	input := "data:{\"id\":\"x\",\n" +
		"data: \"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\": \"stop\"}]}\n\n"
	for _, format := range []string{"anthropic", "responses"} {
		var output strings.Builder
		if err := StreamConvertResponse(strings.NewReader(input), &output, "openai", format); err != nil {
			t.Fatal(err)
		}
		terminal := "message_stop"
		if format == "responses" {
			terminal = "response.completed"
		}
		if !strings.Contains(output.String(), terminal) {
			t.Fatalf("missing %s: %s", terminal, output.String())
		}
	}
}

type rejectingWriter struct{}

func (rejectingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestConversionPropagatesWriteFailure(t *testing.T) {
	input := "data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	for _, format := range []string{"anthropic", "responses"} {
		if err := StreamConvertResponse(strings.NewReader(input), rejectingWriter{}, "openai", format); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("%s: %v", format, err)
		}
	}
}

func TestResponsesLengthLimitIsIncomplete(t *testing.T) {
	input := "data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n"
	var output strings.Builder
	if err := StreamConvertResponse(strings.NewReader(input), &output, "openai", "responses"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "response.incomplete") || strings.Contains(output.String(), "response.completed") {
		t.Fatal(output.String())
	}
}
