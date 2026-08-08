// Package loki implements the Loki log collector (v0.8): it serves the
// adaptive loop's log-evidence requests from a Grafana Loki instance instead
// of (or in addition to) direct pod-log access via kubectl, which is often
// restricted in hardened clusters. Emits the same log.snippet observations
// the analyzers consume, tagged with system "loki".
package loki

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/pkg/api"
)

// DefaultMaxLines caps log lines per target (mirrors the k8s collector).
const DefaultMaxLines = 50

// Collector queries Loki's query_range API for the scope's pod targets.
type Collector struct {
	baseURL string // e.g. http://loki:3100
	client  *http.Client
}

func New(baseURL string) *Collector {
	return &Collector{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Collector) ID() string { return "loki" }

// Collect fetches logs when the scope asks for them (flag or adaptive
// EvidenceRequests with QueryHint "logs"). Failures are silent: the engine
// records the gap, the investigation never fails (roadmap: every collector
// degrades to an evidence gap, never a hard failure).
func (c *Collector) Collect(ctx context.Context, scope *collect.ScopePlan) ([]model.Observation, []model.SourceRef, error) {
	logsWanted := scope.Logs || scope.WantsHint("logs")
	if !logsWanted {
		return nil, nil, nil
	}
	limit := scope.MaxLogLines
	if limit <= 0 {
		limit = DefaultMaxLines
	}

	var obs []model.Observation
	var refs []model.SourceRef
	for _, t := range scope.Targets {
		if t.Kind != "pod" {
			continue
		}
		lines, ts, err := c.queryPod(ctx, t, scope.Window, limit)
		if err != nil || len(lines) == 0 {
			continue // no logs or unreachable -> evidence gap, never fatal
		}
		ref := model.SourceRef{System: "loki", Query: fmt.Sprintf("query_range {namespace=%q} |= %q (limit %d)", t.Namespace, t.Name, limit)}
		obs = append(obs, collect.NewObservation(
			"log.snippet",
			ref,
			ts,
			t,
			map[string]any{"container": "loki", "lines": lines, "line_count": len(lines), "truncated": false},
			1.0,
		))
		refs = append(refs, ref)
	}
	return obs, refs, nil
}

// queryPod runs a LogQL query for one pod's logs over the window.
// Standard promtail setups label streams with namespace and pod name, so we
// query {namespace="<ns>"} |= "<podName>" and filter client-side for the
// exact pod label when present. Backward direction: newest lines first.
func (c *Collector) queryPod(ctx context.Context, pod model.ResourceRef, window api.Window, limit int) ([]string, time.Time, error) {
	q := url.QueryEscape(fmt.Sprintf(`{namespace=%q} |= %q`, pod.Namespace, pod.Name))
	start := window.Start.UnixNano()
	end := window.End.UnixNano()
	if end <= start {
		end = time.Now().UnixNano()
	}
	endpoint := fmt.Sprintf("%s/loki/api/v1/query_range?query=%s&start=%d&end=%d&direction=backward&limit=%d",
		c.baseURL, q, start, end, limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, time.Time{}, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, time.Time{}, fmt.Errorf("loki status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, time.Time{}, err
	}
	return parseResponse(body, limit)
}

type queryRangeResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"` // [unix-ns, line]
		} `json:"result"`
	} `json:"data"`
}

// parseResponse extracts lines, newest first (sorted by timestamp), capped.
// Exported for tests.
func parseResponse(body []byte, limit int) ([]string, time.Time, error) {
	var r queryRangeResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, time.Time{}, err
	}
	type entry struct {
		ts   int64
		line string
	}
	var entries []entry
	var newest time.Time
	for _, stream := range r.Data.Result {
		for _, v := range stream.Values {
			ns, err := strconv.ParseInt(v[0], 10, 64)
			if err != nil {
				continue
			}
			if ts := time.Unix(0, ns); ts.After(newest) {
				newest = ts
			}
			entries = append(entries, entry{ns, v[1]})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ts > entries[j].ts })
	if len(entries) > limit {
		entries = entries[:limit]
	}
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, e.line)
	}
	return lines, newest, nil
}
