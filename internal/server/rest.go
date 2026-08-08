// Package server exposes KubeTective over HTTP (REST) and MCP (stdio) -
// v0.6 roadmap: "REST API + server mode, MCP server (thin wrapper)".
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/GlediLami/kubetective/internal/engine"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/internal/record"
	"github.com/GlediLami/kubetective/pkg/api"
)

// REST is the HTTP API: investigate on demand, list/read incident records.
// The handler is a thin adapter over the same in-process pipeline the CLI
// uses.
type REST struct {
	Inv   api.Investigator
	Store *record.Store
}

func (s *REST) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/investigate", s.handleInvestigate)
	mux.HandleFunc("GET /v1/incidents", s.handleListIncidents)
	mux.HandleFunc("GET /v1/incidents/{id}", s.handleGetIncident)
	// Read-only web UI + self-telemetry (v0.8).
	mux.HandleFunc("GET /", s.handleUI)
	mux.HandleFunc("GET /incidents/{id}", s.handleIncidentUI)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	return withLogging(mux)
}

type investigateRequest struct {
	Target       string `json:"target"`
	Namespace    string `json:"namespace,omitempty"`
	SinceMinutes int    `json:"since_minutes,omitempty"` // 0 = default window
	Logs         *bool  `json:"logs,omitempty"`          // nil = default (on)
}

func (s *REST) handleInvestigate(w http.ResponseWriter, r *http.Request) {
	var body investigateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	target, err := parseRef(body.Target, body.Namespace)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	req := &api.InvestigationRequest{Target: target}
	if body.SinceMinutes > 0 {
		req.Window = api.Window{Start: time.Now().UTC().Add(-time.Duration(body.SinceMinutes) * time.Minute), End: time.Now().UTC()}
	}
	if body.Logs != nil {
		req.Scope = api.ScopeOptions{Logs: *body.Logs}
	}
	res, err := s.Inv.Investigate(r.Context(), req)
	if err != nil {
		investigationErr.Add(1)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	investigations.Add(1)
	investigationsOK.Add(1)
	// Record every investigation (replay substrate) -
	// same behavior as the CLI.
	if s.Store != nil {
		inc := record.BuildIncident(engine.Version, req, res)
		if id, serr := s.Store.Save(inc); serr == nil {
			res.Meta.RecordID = id
		}
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *REST) handleListIncidents(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeErr(w, http.StatusNotFound, "no incident store configured")
		return
	}
	ids, err := s.Store.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"incidents": ids})
}

func (s *REST) handleGetIncident(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeErr(w, http.StatusNotFound, "no incident store configured")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "missing incident id")
		return
	}
	inc, err := s.Store.Load(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("incident %q: %v", id, err))
		return
	}
	writeJSON(w, http.StatusOK, inc)
}

// parseRef accepts "kind/name", "namespace/kind/name", or "kind/ns:name"
// forms plus a namespace override.
func parseRef(raw, namespace string) (model.ResourceRef, error) {
	ref := model.ResourceRef{Namespace: namespace}
	parts := strings.Split(raw, "/")
	switch len(parts) {
	case 2:
		ref.Kind, ref.Name = parts[0], parts[1]
	case 3:
		ref.Namespace, ref.Kind, ref.Name = parts[0], parts[1], parts[2]
	default:
		return ref, fmt.Errorf("target %q: want kind/name or namespace/kind/name", raw)
	}
	if ref.Kind == "" || ref.Name == "" {
		return ref, fmt.Errorf("target %q: empty kind or name", raw)
	}
	return ref, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		fmt.Printf("%s %s %s (%s)\n", r.Method, r.URL.Path, r.RemoteAddr, time.Since(start).Round(time.Millisecond))
	})
}
