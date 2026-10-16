package main

import "github.com/prometheus/client_golang/prometheus"

// Metrics of the mock, labelled by pipeline step and outcome
// (ok, empty, 429, 402, 5xx, bad_request, cancelled).
type Metrics struct {
	Requests *prometheus.CounterVec
	Duration *prometheus.HistogramVec
	Tokens   *prometheus.CounterVec
	InFlight prometheus.Gauge
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mock_llm_requests_total",
			Help: "Chat completion requests by pipeline step and outcome.",
		}, []string{"step", "outcome"}),
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "mock_llm_request_duration_seconds",
			Help:    "Time to answer a chat completion, injected latency included.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20, 40, 60, 120, 300},
		}, []string{"step", "outcome"}),
		Tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mock_llm_tokens_total",
			Help: "Tokens reported in the usage block, by step and direction (prompt, completion).",
		}, []string{"step", "direction"}),
		InFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mock_llm_in_flight_requests",
			Help: "Chat completion requests being answered.",
		}),
	}
	reg.MustRegister(m.Requests, m.Duration, m.Tokens, m.InFlight)
	return m
}
