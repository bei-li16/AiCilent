package adapter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type AnthropicRequest struct {
	Model         string             `json:"model"`
	MaxTokens     int                `json:"max_tokens"`
	Messages      []AnthropicMessage `json:"messages"`
	System        string             `json:"system,omitempty"`
	Temperature   float64            `json:"temperature,omitempty"`
	TopP          float64            `json:"top_p,omitempty"`
	Stream        bool               `json:"stream,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
	Metadata      map[string]string  `json:"metadata,omitempty"`
}

type AnthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type AnthropicResponse struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Role       string             `json:"role"`
	Model      string             `json:"model"`
	Content    []AnthropicContent `json:"content"`
	StopReason string             `json:"stop_reason"`
	Usage      *AnthropicUsage    `json:"usage,omitempty"`
}

type AnthropicContent struct {
	Type   string      `json:"type"`
	Text   string      `json:"text,omitempty"`
	ID     string      `json:"id,omitempty"`
	Name   string      `json:"name,omitempty"`
	Input  interface{} `json:"input,omitempty"`
	Thinking string   `json:"thinking,omitempty"`
}

type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func CallAnthropic(baseURL, apiKey, model string, req AnthropicRequest, timeout int) (*AnthropicResponse, error) {
	req.Model = model
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	respBody, err := postAnthropic(baseURL+"/messages", apiKey, body, timeout)
	if err != nil {
		return nil, err
	}

	var result AnthropicResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &result, nil
}

func CallAnthropicRaw(baseURL, apiKey string, body []byte, timeout int) ([]byte, error) {
	return postAnthropic(baseURL+"/messages", apiKey, body, timeout)
}

func postAnthropic(url, apiKey string, body []byte, timeout int) ([]byte, error) {
	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: time.Duration(timeout) * time.Second}
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