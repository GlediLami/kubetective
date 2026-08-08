// Package llm implements the optional LLM layer: a provider abstraction with
// an OpenAI-compatible adapter (OpenAI, Ollama, vLLM, llama.cpp), a redacted
// structured digest builder, and the constrained explainer.
//
// Design constraints enforced here:
//   - the LLM receives ONLY the redacted digest — never raw observations,
//     logs, kubeconfig, or secrets;
//   - the LLM can never change scores, emit CAUSED_BY, or propose actions
//     outside the deterministic recommendation table;
//   - output is strict JSON, validated and sanitized by the caller.
package llm

import (
	"context"
	"errors"
)

// ErrNotConfigured is returned when no provider is configured.
var ErrNotConfigured = errors.New("llm: no provider configured")

// Request is one chat completion call.
type Request struct {
	System string
	User   string
	// JSON requests a JSON object response (response_format where supported).
	JSON bool
}

// Provider is the minimal LLM surface KubeTective needs.
type Provider interface {
	Complete(ctx context.Context, req Request) (string, error)
}
