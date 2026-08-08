package adapter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

type OpenAIRequest struct {
	Model       string          `json:"model"`
	Messages    []OpenAIMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
	TopP        float64         `json:"top_p,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	Stop        []string        `json:"stop,omitempty"`
}

type OpenAIMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []OpenAIToolCall `json:"tool_calls,omitempty"`
}

type OpenAIToolCall struct {
	Index    int            `json:"index,omitempty"`
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function OpenAIFunction `json:"function"`
}

type OpenAIFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func CallOpenAIRaw(baseURL, apiKey string, body []byte, timeout int, transport http.RoundTripper) ([]byte, error) {
	return CallOpenAIRawContext(context.Background(), baseURL, apiKey, body, timeout, "", transport)
}

func CallOpenAIRawContext(ctx context.Context, baseURL, apiKey string, body []byte, timeout int, requestID string, transport http.RoundTripper) ([]byte, error) {
	return postOpenAI(ctx, baseURL+"/chat/completions", apiKey, body, timeout, requestID, transport)
}

func postOpenAI(ctx context.Context, url, apiKey string, body []byte, timeout int, requestID string, transport http.RoundTripper) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	if requestID != "" {
		httpReq.Header.Set("X-Request-ID", requestID)
	}

	client := &http.Client{
		Timeout:   time.Duration(timeout) * time.Second,
		Transport: transport,
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode, Message: string(respBody)}
	}

	return respBody, nil
}
