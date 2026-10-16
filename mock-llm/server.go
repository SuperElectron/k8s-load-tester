package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const maxBodyBytes = 256 << 20

// Server answers OpenAI-compatible chat completions with canned pipeline replies.
type Server struct {
	cfg     Config
	rand    *Rand
	metrics *Metrics
	replier replier
	now     func() time.Time
	sleep   func(context.Context, time.Duration) error
	log     *slog.Logger
}

func NewServer(cfg Config, reg prometheus.Registerer, gatherer prometheus.Gatherer, log *slog.Logger) (*Server, http.Handler) {
	r := NewRand(cfg.Seed)
	s := &Server{
		cfg:     cfg,
		rand:    r,
		metrics: NewMetrics(reg),
		now:     time.Now,
		sleep:   sleepCtx,
		log:     log,
	}
	s.replier = replier{docTypes: cfg.DocTypes, rand: r, now: func() time.Time { return s.now() }}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.chatCompletions)
	mux.HandleFunc("POST /chat/completions", s.chatCompletions)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))
	return s, mux
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// fault is the injected outcome of one request.
type fault int

const (
	faultNone fault = iota
	fault429
	fault402
	fault5xx
	faultEmpty
)

func (s *Server) drawFault() fault {
	x := s.rand.Float64()
	for _, f := range []struct {
		rate float64
		f    fault
	}{{s.cfg.Rate429, fault429}, {s.cfg.Rate402, fault402}, {s.cfg.Rate5xx, fault5xx}, {s.cfg.RateEmpty, faultEmpty}} {
		if x < f.rate {
			return f.f
		}
		x -= f.rate
	}
	return faultNone
}

func (s *Server) chatCompletions(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	s.metrics.InFlight.Inc()
	defer s.metrics.InFlight.Dec()

	step, outcome := stepUnknown, "bad_request"
	defer func() {
		s.metrics.Requests.WithLabelValues(step, outcome).Inc()
		s.metrics.Duration.WithLabelValues(step, outcome).Observe(time.Since(started).Seconds())
	}()

	var req chatRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("invalid JSON body: %v", err), nil)
		return
	}
	if req.Stream {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "stream=true is not supported by the mock", nil)
		return
	}
	p, err := parsePrompt(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error(), nil)
		return
	}
	step = detectStep(p)

	retryAfter := strconv.Itoa(int(s.cfg.RetryAfter / time.Second))
	switch s.drawFault() {
	case fault429:
		outcome = "429"
		w.Header().Set("Retry-After", retryAfter)
		writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded", "Rate limit exceeded (mock)", 429)
		return
	case fault402:
		outcome = "402"
		w.Header().Set("Retry-After", retryAfter)
		writeError(w, http.StatusPaymentRequired, "in_flight_budget_exhausted",
			"in_flight_budget_exhausted: too many requests in flight for this key (mock)", 402)
		return
	case fault5xx:
		outcome = "5xx"
		writeError(w, s.cfg.Status5xx, "server_error", "Upstream error (mock)", s.cfg.Status5xx)
		return
	case faultEmpty:
		outcome = "empty"
		if err := s.sleep(r.Context(), s.cfg.Latency.Sample(s.rand)); err != nil {
			outcome = "cancelled"
			return
		}
		s.writeCompletion(w, req, step, p, "")
		return
	}

	content, err := s.replier.reply(step, p, r.Header.Get("X-Mock-Doc-Type"))
	if err != nil {
		s.log.Warn("no reply for prompt", "step", step, "err", err, "tenant", r.Header.Get("X-Koone-Tenant"))
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error(), nil)
		return
	}
	if err := s.sleep(r.Context(), s.cfg.Latency.Sample(s.rand)); err != nil {
		outcome = "cancelled"
		return
	}
	outcome = "ok"
	s.writeCompletion(w, req, step, p, content)
}

func (s *Server) writeCompletion(w http.ResponseWriter, req chatRequest, step string, p prompt, content string) {
	in, out := promptTokens(p), completionTokens(content)
	s.metrics.Tokens.WithLabelValues(step, "prompt").Add(float64(in))
	s.metrics.Tokens.WithLabelValues(step, "completion").Add(float64(out))
	model := req.Model
	if model == "" {
		model = "mock"
	}
	now := s.now()
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      fmt.Sprintf("chatcmpl-mock-%d", now.UnixNano()),
		"object":  "chat.completion",
		"created": now.Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     in,
			"completion_tokens": out,
			"total_tokens":      in + out,
		},
	})
}

func writeError(w http.ResponseWriter, status int, typ, message string, code any) {
	writeJSON(w, status, map[string]any{"error": map[string]any{
		"message": message,
		"type":    typ,
		"code":    code,
	}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
