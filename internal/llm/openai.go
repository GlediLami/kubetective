package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAICompatible speaks the /v1/chat/completions protocol — the common
// denominator of OpenAI, Ollama, vLLM, and llama.cpp servers.
type OpenAICompatible struct {
	baseURL string // e.g. https://api.openai.com/v1 or http://localhost:11434/v1
	model   string
	apiKey  string // optional (Ollama/local don't need one)
	client  *http.Client
}

var _ Provider = (*OpenAICompatible)(nil)

// NewOpenAICompatible creates an adapter for any OpenAI-compatible endpoint.
func NewOpenAICompatible(baseURL, model, apiKey string) *OpenAICompatible {
	return &OpenAICompatible{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Temperature    float64       `json:"temperature"`
	ResponseFormat *responseFmt  `json:"response_format,omitempty"`
}

type responseFmt struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (o *OpenAICompatible) Complete(ctx context.Context, req Request) (string, error) {
	if o.baseURL == "" || o.model == "" {
		return "", ErrNotConfigured
	}
	body := chatRequest{
		Model:       o.model,
		Temperature: 0.2,
		Messages: []chatMessage{
			{Role: "system", Content: req.System},
			{Role: "user", Content: req.User},
		},
	}
	if req.JSON {
		body.ResponseFormat = &responseFmt{Type: "json_object"}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm provider returned %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}
	var cr chatResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return "", fmt.Errorf("llm response parse: %w", err)
	}
	if cr.Error != nil && cr.Error.Message != "" {
		return "", fmt.Errorf("llm provider error: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 || strings.TrimSpace(cr.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("llm provider returned no content")
	}
	return cr.Choices[0].Message.Content, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
