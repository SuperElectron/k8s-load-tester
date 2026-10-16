package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func newTestServer(t *testing.T, mutate func(*Config)) (*Server, *httptest.Server) {
	t.Helper()
	cfg, err := LoadConfig(nil, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	cfg.Seed = 7
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	s, h := NewServer(cfg, reg, reg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.now = fixedNow
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return s, ts
}

func classifyBody(t *testing.T) []byte {
	t.Helper()
	pdf := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 <</Type /Page>> <</Type /Page>>"))
	body, _ := json.Marshal(map[string]any{
		"model": "google/gemini-2.5-flash",
		"messages": []any{
			map[string]any{"role": "system", "content": "You are an expert in classifying Italian administrative and legal documents."},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "Classify this document type:"},
				map[string]any{"type": "file", "file": map[string]any{
					"filename": "mocktype-unilav-1.pdf", "file_data": "data:application/pdf;base64," + pdf}},
			}},
		},
	})
	return body
}

func post(t *testing.T, ts *httptest.Server, body []byte, header map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return resp, out
}

func content(t *testing.T, out map[string]any) string {
	t.Helper()
	choices := out["choices"].([]any)
	return choices[0].(map[string]any)["message"].(map[string]any)["content"].(string)
}

func TestChatCompletionOK(t *testing.T) {
	_, ts := newTestServer(t, nil)
	resp, out := post(t, ts, classifyBody(t), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %v", resp.StatusCode, out)
	}
	if got := content(t, out); got != "unilav" {
		t.Fatalf("content %q", got)
	}
	if out["model"] != "google/gemini-2.5-flash" || out["object"] != "chat.completion" {
		t.Fatalf("envelope %v", out)
	}
	usage := out["usage"].(map[string]any)
	in, done, total := usage["prompt_tokens"].(float64), usage["completion_tokens"].(float64), usage["total_tokens"].(float64)
	if in < 2*tokensPerPDFPage || done < 1 || total != in+done {
		t.Fatalf("usage %v", usage)
	}
}

func TestHeaderOverridesClassify(t *testing.T) {
	_, ts := newTestServer(t, nil)
	_, out := post(t, ts, classifyBody(t), map[string]string{"X-Mock-Doc-Type": "psc"})
	if got := content(t, out); got != "psc" {
		t.Fatalf("content %q", got)
	}
}

func TestInjectedFaults(t *testing.T) {
	cases := []struct {
		name   string
		set    func(*Config)
		status int
		check  func(*testing.T, *http.Response, map[string]any)
	}{
		{"429", func(c *Config) { c.Rate429 = 1; c.RetryAfter = 3 * time.Second }, 429,
			func(t *testing.T, r *http.Response, _ map[string]any) {
				if r.Header.Get("Retry-After") != "3" {
					t.Errorf("Retry-After %q", r.Header.Get("Retry-After"))
				}
			}},
		{"402", func(c *Config) { c.Rate402 = 1 }, 402,
			func(t *testing.T, r *http.Response, out map[string]any) {
				b, _ := json.Marshal(out)
				if !strings.Contains(string(b), "in_flight_budget_exhausted") {
					t.Errorf("body %s", b)
				}
				if r.Header.Get("Retry-After") != "1" {
					t.Errorf("Retry-After %q", r.Header.Get("Retry-After"))
				}
			}},
		{"5xx", func(c *Config) { c.Rate5xx = 1; c.Status5xx = 502 }, 502, nil},
		{"empty", func(c *Config) { c.RateEmpty = 1 }, 200,
			func(t *testing.T, _ *http.Response, out map[string]any) {
				if got := content(t, out); got != "" {
					t.Errorf("content %q", got)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ts := newTestServer(t, tc.set)
			resp, out := post(t, ts, classifyBody(t), nil)
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d want %d: %v", resp.StatusCode, tc.status, out)
			}
			if tc.check != nil {
				tc.check(t, resp, out)
			}
		})
	}
}

func TestFaultRateShares(t *testing.T) {
	s, _ := newTestServer(t, func(c *Config) { c.Rate429 = 0.1; c.Rate402 = 0.2; c.Rate5xx = 0.05; c.RateEmpty = 0.05 })
	counts := map[fault]int{}
	const n = 20000
	for range n {
		counts[s.drawFault()]++
	}
	for f, want := range map[fault]float64{fault429: 0.1, fault402: 0.2, fault5xx: 0.05, faultEmpty: 0.05, faultNone: 0.6} {
		if got := float64(counts[f]) / n; math.Abs(got-want) > 0.015 {
			t.Errorf("fault %d share %.3f want %.2f", f, got, want)
		}
	}
}

func TestBadRequests(t *testing.T) {
	_, ts := newTestServer(t, nil)
	for name, body := range map[string]string{
		"not json":       `{`,
		"stream":         `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		"unknown prompt": `{"model":"m","messages":[{"role":"system","content":"You are helpful."},{"role":"user","content":"hi"}]}`,
	} {
		resp, out := post(t, ts, []byte(body), nil)
		if resp.StatusCode != 400 || out["error"] == nil {
			t.Errorf("%s: status %d %v", name, resp.StatusCode, out)
		}
	}
}

func TestHealthAndMetrics(t *testing.T) {
	_, ts := newTestServer(t, nil)
	post(t, ts, classifyBody(t), nil)

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("healthz: %v %v", err, resp)
	}
	resp, err = http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{
		`mock_llm_requests_total{outcome="ok",step="classify"} 1`,
		`mock_llm_request_duration_seconds_count{outcome="ok",step="classify"} 1`,
		`mock_llm_tokens_total{direction="prompt",step="classify"}`,
		`mock_llm_in_flight_requests 0`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

func TestLatencyIsApplied(t *testing.T) {
	s, ts := newTestServer(t, func(c *Config) { c.Latency = Latency{Kind: "fixed", FixedMs: 250} })
	var slept time.Duration
	s.sleep = func(_ context.Context, d time.Duration) error { slept = d; return nil }
	post(t, ts, classifyBody(t), nil)
	if slept != 250*time.Millisecond {
		t.Fatalf("slept %v", slept)
	}
}

func TestLatencyDistributions(t *testing.T) {
	r := NewRand(3)
	if d := (Latency{Kind: "fixed", FixedMs: 40}).Sample(r); d != 40*time.Millisecond {
		t.Errorf("fixed %v", d)
	}
	u := Latency{Kind: "uniform", MinMs: 100, MaxMs: 200}
	for range 1000 {
		if d := u.Sample(r); d < 100*time.Millisecond || d > 200*time.Millisecond {
			t.Fatalf("uniform out of range: %v", d)
		}
	}
	ln := Latency{Kind: "lognormal", MedianMs: 1000, Sigma: 0.6, MaxMs: 5000}
	samples := make([]float64, 5001)
	for i := range samples {
		d := ln.Sample(r)
		if d > 5*time.Second {
			t.Fatalf("lognormal above cap: %v", d)
		}
		samples[i] = float64(d / time.Millisecond)
	}
	sort.Float64s(samples)
	if med := samples[len(samples)/2]; med < 930 || med > 1070 {
		t.Errorf("lognormal median %.0fms, want ~1000", med)
	}
}

func TestConfigFromEnvAndFlags(t *testing.T) {
	env := map[string]string{"MOCK_LATENCY": "lognormal", "MOCK_LATENCY_MEDIAN_MS": "800", "MOCK_RATE_429": "0.1",
		"MOCK_DOC_TYPES": "durc=5, unilav=3,psc"}
	cfg, err := LoadConfig([]string{"-rate-429", "0.2"}, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Rate429 != 0.2 || cfg.Latency.Kind != "lognormal" || cfg.Latency.MedianMs != 800 {
		t.Fatalf("cfg %+v", cfg)
	}
	want := []WeightedType{{"durc", 5}, {"unilav", 3}, {"psc", 1}}
	if len(cfg.DocTypes) != 3 || cfg.DocTypes[0] != want[0] || cfg.DocTypes[1] != want[1] || cfg.DocTypes[2] != want[2] {
		t.Fatalf("doc types %v", cfg.DocTypes)
	}
}

func TestConfigRejects(t *testing.T) {
	for name, args := range map[string][]string{
		"rates over 1":     {"-rate-429", "0.6", "-rate-5xx", "0.6"},
		"negative rate":    {"-rate-empty", "-0.1"},
		"bad latency kind": {"-latency", "gamma"},
		"uniform bounds":   {"-latency", "uniform", "-latency-min-ms", "10", "-latency-max-ms", "5"},
		"lognormal median": {"-latency", "lognormal"},
		"bad status":       {"-status-5xx", "404"},
		"bad number":       {"-rate-429", "lots"},
		"bad doc type":     {"-doc-types", "DURC"},
		"bad weight":       {"-doc-types", "durc=0"},
	} {
		if _, err := LoadConfig(args, func(string) string { return "" }); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}
