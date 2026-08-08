package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAICompatibleComplete(t *testing.T) {
	var gotBody chatRequest
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s, want /v1/chat/completions", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"summary\":\"hi\"}"}}]}`)
	}))
	defer srv.Close()

	p := NewOpenAICompatible(srv.URL+"/v1", "test-model", "sk-secret")
	out, err := p.Complete(context.Background(), Request{
		System: "sys", User: "usr", JSON: true,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out != `{"summary":"hi"}` {
		t.Errorf("content = %q", out)
	}
	if gotBody.Model != "test-model" {
		t.Errorf("model = %q", gotBody.Model)
	}
	if len(gotBody.Messages) != 2 || gotBody.Messages[0].Content != "sys" || gotBody.Messages[1].Content != "usr" {
		t.Errorf("messages = %+v", gotBody.Messages)
	}
	if gotBody.ResponseFormat == nil || gotBody.ResponseFormat.Type != "json_object" {
		t.Errorf("response_format = %+v, want json_object", gotBody.ResponseFormat)
	}
	if gotAuth != "Bearer sk-secret" {
		t.Errorf("auth = %q", gotAuth)
	}
}

func TestOpenAICompatibleErrors(t *testing.T) {
	// Unconfigured.
	if _, err := NewOpenAICompatible("", "", "").Complete(context.Background(), Request{}); err != ErrNotConfigured {
		t.Fatalf("unconfigured error = %v, want ErrNotConfigured", err)
	}
	// Non-200.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"rate limited"}}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()
	if _, err := NewOpenAICompatible(srv.URL+"/v1", "m", "").Complete(context.Background(), Request{}); err == nil {
		t.Fatal("expected error on 429")
	}
	// Empty choices.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[]}`)
	}))
	defer srv2.Close()
	if _, err := NewOpenAICompatible(srv2.URL+"/v1", "m", "").Complete(context.Background(), Request{}); err == nil || !strings.Contains(err.Error(), "no content") {
		t.Fatalf("empty choices error = %v", err)
	}
	// Provider-level error field.
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"error":{"message":"model not found"}}`)
	}))
	defer srv3.Close()
	if _, err := NewOpenAICompatible(srv3.URL+"/v1", "m", "").Complete(context.Background(), Request{}); err == nil {
		t.Fatal("expected provider error")
	}
}
