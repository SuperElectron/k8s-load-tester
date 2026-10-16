package main

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sync"
	"time"
)

// Rand is a seeded source safe for concurrent requests.
type Rand struct {
	mu sync.Mutex
	r  *rand.Rand
}

func NewRand(seed uint64) *Rand {
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	return &Rand{r: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))}
}

func (r *Rand) Float64() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.r.Float64()
}

func (r *Rand) NormFloat64() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.r.NormFloat64()
}

// Latency is the delay distribution of a successful reply.
type Latency struct {
	Kind     string
	FixedMs  float64
	MinMs    float64
	MaxMs    float64
	MedianMs float64
	Sigma    float64
}

func (l Latency) Validate() error {
	switch l.Kind {
	case "fixed":
		if l.FixedMs < 0 {
			return fmt.Errorf("-latency-ms must not be negative")
		}
	case "uniform":
		if l.MinMs < 0 || l.MaxMs < l.MinMs {
			return fmt.Errorf("uniform latency needs 0 <= -latency-min-ms <= -latency-max-ms")
		}
	case "lognormal":
		if l.MedianMs <= 0 || l.Sigma < 0 || l.MaxMs < 0 {
			return fmt.Errorf("lognormal latency needs -latency-median-ms > 0, -latency-sigma >= 0, -latency-max-ms >= 0")
		}
	default:
		return fmt.Errorf("-latency must be fixed, uniform or lognormal, got %q", l.Kind)
	}
	return nil
}

// Sample draws one delay.
func (l Latency) Sample(r *Rand) time.Duration {
	var ms float64
	switch l.Kind {
	case "fixed":
		ms = l.FixedMs
	case "uniform":
		ms = l.MinMs + r.Float64()*(l.MaxMs-l.MinMs)
	case "lognormal":
		ms = l.MedianMs * math.Exp(l.Sigma*r.NormFloat64())
		if l.MaxMs > 0 && ms > l.MaxMs {
			ms = l.MaxMs
		}
	}
	return time.Duration(ms * float64(time.Millisecond))
}
