// Command mock-llm is an OpenAI-compatible chat completions server that
// answers the document pipeline's prompts with canned, schema-valid JSON,
// with configurable latency and injected failures, for load tests.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

func main() {
	cfg := mustLoadConfig()
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	_, handler := NewServer(cfg, reg, reg, log)

	srv := &http.Server{Addr: cfg.Addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Info("mock-llm listening", "addr", cfg.Addr, "latency", cfg.Latency.Kind,
		"rate_429", cfg.Rate429, "rate_402", cfg.Rate402, "rate_5xx", cfg.Rate5xx, "rate_empty", cfg.RateEmpty)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server failed", "err", err)
		os.Exit(1)
	}
}
