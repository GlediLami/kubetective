// Package prometheus implements the Prometheus collector: it queries the
// Prometheus HTTP API (query_range) for per-container resource series over the
// investigation window and normalizes them into compact metric.series
// observations. Raw series never leave this package; analyzers derive
// breaches and growth from the summaries.
//
// The collector is optional and failure-tolerant: an unreachable Prometheus
// becomes an EvidenceGap, never a failed investigation.
package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/model"
)

// maxPods caps how many pods from Prior are queried (large-cluster guard).
const maxPods = 50

// step is the query_range resolution over the window.
const step = 30 * time.Second

// Metric names this collector knows how to fetch (v0.2 scope).
const (
	MetricMemory = "container_memory_working_set_bytes"
	MetricThrottle = "container_cpu_cfs_throttled_periods_total"
	MetricCPUUsage = "container_cpu_usage_seconds_total"
)

// Collector queries one Prometheus instance.
type Collector struct {
	baseURL string
	client  *http.Client
}

var _ collect.Collector = (*Collector)(nil)

// New creates a collector for the given Prometheus base URL
// (e.g. http://localhost:9090).
func New(baseURL string) *Collector {
	return &Collector{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Collector) ID() string { return "prometheus" }

// queryRangeResult is the subset of the Prometheus API response we consume.
type queryRangeResult struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Values [][2]any          `json:"values"` // [unix_seconds, "value"]
		} `json:"result"`
	} `json:"data"`
}

func (c *Collector) Collect(ctx context.Context, scope *collect.ScopePlan) ([]model.Observation, []model.SourceRef, error) {
	if c.baseURL == "" {
		return nil, nil, nil
	}
	// Derive pod targets from earlier collectors (staged collection): the
	// k8s collector's pod.state observations tell us which pods exist.
	var pods []model.ResourceRef
	for _, o := range scope.Prior {
		if o.Kind == "pod.state" && o.Resource.Kind == "pod" {
			pods = append(pods, o.Resource)
			if len(pods) >= maxPods {
				break
			}
		}
	}
	if len(pods) == 0 {
		return nil, nil, fmt.Errorf("prometheus collector: no pod targets in prior observations (run the kubernetes collector first)")
	}

	var obs []model.Observation
	var refs []model.SourceRef
	for _, pod := range pods {
		for _, metric := range []string{MetricMemory, MetricThrottle, MetricCPUUsage} {
			query := fmt.Sprintf(`sum by (container) (%s{namespace=%q, pod=%q})`, metric, pod.Namespace, pod.Name)
			o, ref, err := c.fetchSeries(ctx, scope, query, metric, pod)
			if err != nil {
				return nil, refs, err
			}
			refs = append(refs, ref)
			obs = append(obs, o...)
		}
	}
	return obs, refs, nil
}

// fetchSeries runs one query_range and emits one metric.series observation
// per container: a compact {first, last, min, max, count} summary that
// analyzers turn into breach/growth evidence.
func (c *Collector) fetchSeries(ctx context.Context, scope *collect.ScopePlan, query, metric string, pod model.ResourceRef) ([]model.Observation, model.SourceRef, error) {
	ref := model.SourceRef{System: "prometheus", Query: query}
	start, end := scope.Window.Start, scope.Window.End
	if start.IsZero() {
		start = end.Add(-30 * time.Minute)
	}
	if end.IsZero() {
		end = time.Now()
	}

	u := fmt.Sprintf("%s/api/v1/query_range?query=%s&start=%d&end=%d&step=%ds",
		c.baseURL, url.QueryEscape(query), start.Unix(), end.Unix(), int(step.Seconds()))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, ref, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, ref, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, ref, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, ref, fmt.Errorf("prometheus query failed (%d): %s", resp.StatusCode, truncate(string(body), 200))
	}
	var qr queryRangeResult
	if err := json.Unmarshal(body, &qr); err != nil {
		return nil, ref, fmt.Errorf("prometheus response parse: %w", err)
	}
	if qr.Status != "success" {
		return nil, ref, fmt.Errorf("prometheus status=%s", qr.Status)
	}

	var obs []model.Observation
	for _, series := range qr.Data.Result {
		container := series.Metric["container"]
		if container == "" {
			continue
		}
		sum := seriesSummary(series.Values)
		if sum.count == 0 {
			continue
		}
		obs = append(obs, collect.NewObservation(
			"metric.series",
			ref,
			sum.lastTs,
			pod,
			map[string]any{
				"metric":    metric,
				"container": container,
				"first":     sum.first,
				"last":      sum.last,
				"min":       sum.min,
				"max":       sum.max,
				"count":     sum.count,
				"unit":      unitFor(metric),
			},
			1.0,
		))
	}
	return obs, ref, nil
}

type summary struct {
	first, last, min, max float64
	lastTs                time.Time
	count                 int
}

func seriesSummary(values [][2]any) summary {
	s := summary{min: 1e300, max: -1e300}
	for _, v := range values {
		ts := int64(0)
		if f, ok := v[0].(float64); ok {
			ts = int64(f)
		}
		val := 0.0
		if f, ok := v[1].(float64); ok {
			val = f
		} else if str, ok := v[1].(string); ok {
			val, _ = strconv.ParseFloat(str, 64)
		}
		if s.count == 0 {
			s.first = val
		}
		s.last = val
		s.lastTs = time.Unix(ts, 0).UTC()
		if val < s.min {
			s.min = val
		}
		if val > s.max {
			s.max = val
		}
		s.count++
	}
	if s.count == 0 {
		s.min, s.max = 0, 0
	}
	return s
}

func unitFor(metric string) string {
	switch metric {
	case MetricMemory:
		return "bytes"
	case MetricThrottle, MetricCPUUsage:
		return "seconds"
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
