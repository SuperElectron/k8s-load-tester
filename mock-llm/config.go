package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is everything the mock can be told at start-up. Every flag has an
// environment variable twin; the flag wins when both are set.
type Config struct {
	Addr string

	Latency Latency

	Rate429   float64
	Rate402   float64
	Rate5xx   float64
	RateEmpty float64

	RetryAfter time.Duration
	Status5xx  int

	// DocTypes is the weighted mix classify answers from when the request
	// names no type itself.
	DocTypes []WeightedType

	// Seed of the random source; 0 seeds from the clock.
	Seed uint64
}

// WeightedType is one entry of the classify mix.
type WeightedType struct {
	Name   string
	Weight float64
}

type option struct {
	flag, env, def, usage string
	value                 *string
}

// LoadConfig reads flags from args, falling back to the environment, then to defaults.
func LoadConfig(args []string, getenv func(string) string) (Config, error) {
	opts := []*option{
		{flag: "addr", env: "MOCK_ADDR", def: ":8080", usage: "listen address"},
		{flag: "latency", env: "MOCK_LATENCY", def: "fixed", usage: "latency distribution: fixed | uniform | lognormal"},
		{flag: "latency-ms", env: "MOCK_LATENCY_MS", def: "0", usage: "fixed latency in ms"},
		{flag: "latency-min-ms", env: "MOCK_LATENCY_MIN_MS", def: "0", usage: "uniform: lower bound in ms"},
		{flag: "latency-max-ms", env: "MOCK_LATENCY_MAX_MS", def: "0", usage: "uniform: upper bound in ms; lognormal: cap in ms (0 = no cap)"},
		{flag: "latency-median-ms", env: "MOCK_LATENCY_MEDIAN_MS", def: "0", usage: "lognormal: median in ms"},
		{flag: "latency-sigma", env: "MOCK_LATENCY_SIGMA", def: "0.5", usage: "lognormal: sigma of the underlying normal"},
		{flag: "rate-429", env: "MOCK_RATE_429", def: "0", usage: "fraction of requests answered 429 (0..1)"},
		{flag: "rate-402", env: "MOCK_RATE_402", def: "0", usage: "fraction answered 402 in_flight_budget_exhausted (0..1)"},
		{flag: "rate-5xx", env: "MOCK_RATE_5XX", def: "0", usage: "fraction answered with the 5xx status (0..1)"},
		{flag: "rate-empty", env: "MOCK_RATE_EMPTY", def: "0", usage: "fraction answered 200 with empty content (0..1)"},
		{flag: "retry-after-s", env: "MOCK_RETRY_AFTER_S", def: "1", usage: "Retry-After seconds on 429/402"},
		{flag: "status-5xx", env: "MOCK_STATUS_5XX", def: "503", usage: "status code of injected server errors (500..599)"},
		{flag: "doc-types", env: "MOCK_DOC_TYPES", def: "durc", usage: "classify mix, e.g. durc=5,unilav=3,psc=1"},
		{flag: "seed", env: "MOCK_SEED", def: "0", usage: "random seed (0 = from clock)"},
	}
	fs := flag.NewFlagSet("mock-llm", flag.ContinueOnError)
	for _, o := range opts {
		def := o.def
		if v := getenv(o.env); v != "" {
			def = v
		}
		o.value = fs.String(o.flag, def, fmt.Sprintf("%s (env %s)", o.usage, o.env))
	}
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	get := func(name string) string {
		for _, o := range opts {
			if o.flag == name {
				return strings.TrimSpace(*o.value)
			}
		}
		panic("unknown option " + name)
	}

	var errs []error
	num := func(name string) float64 {
		v, err := strconv.ParseFloat(get(name), 64)
		if err != nil {
			errs = append(errs, fmt.Errorf("-%s: %w", name, err))
		}
		return v
	}
	integer := func(name string) int {
		v, err := strconv.Atoi(get(name))
		if err != nil {
			errs = append(errs, fmt.Errorf("-%s: %w", name, err))
		}
		return v
	}

	cfg := Config{
		Addr: get("addr"),
		Latency: Latency{
			Kind:     get("latency"),
			FixedMs:  num("latency-ms"),
			MinMs:    num("latency-min-ms"),
			MaxMs:    num("latency-max-ms"),
			MedianMs: num("latency-median-ms"),
			Sigma:    num("latency-sigma"),
		},
		Rate429:    num("rate-429"),
		Rate402:    num("rate-402"),
		Rate5xx:    num("rate-5xx"),
		RateEmpty:  num("rate-empty"),
		RetryAfter: time.Duration(integer("retry-after-s")) * time.Second,
		Status5xx:  integer("status-5xx"),
	}
	seed, err := strconv.ParseUint(get("seed"), 10, 64)
	if err != nil {
		errs = append(errs, fmt.Errorf("-seed: %w", err))
	}
	cfg.Seed = seed
	types, err := parseDocTypes(get("doc-types"))
	if err != nil {
		errs = append(errs, err)
	}
	cfg.DocTypes = types
	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, cfg.Validate()
}

// Validate rejects a configuration the mock cannot honour.
func (c Config) Validate() error {
	var errs []error
	for name, r := range map[string]float64{"rate-429": c.Rate429, "rate-402": c.Rate402, "rate-5xx": c.Rate5xx, "rate-empty": c.RateEmpty} {
		if r < 0 || r > 1 {
			errs = append(errs, fmt.Errorf("-%s must be within 0..1, got %v", name, r))
		}
	}
	if sum := c.Rate429 + c.Rate402 + c.Rate5xx + c.RateEmpty; sum > 1 {
		errs = append(errs, fmt.Errorf("fault rates add up to %v, more than 1", sum))
	}
	if c.Status5xx < 500 || c.Status5xx > 599 {
		errs = append(errs, fmt.Errorf("-status-5xx must be 500..599, got %d", c.Status5xx))
	}
	if c.RetryAfter < 0 {
		errs = append(errs, fmt.Errorf("-retry-after-s must not be negative"))
	}
	if err := c.Latency.Validate(); err != nil {
		errs = append(errs, err)
	}
	if len(c.DocTypes) == 0 {
		errs = append(errs, fmt.Errorf("-doc-types must name at least one type"))
	}
	return errors.Join(errs...)
}

// parseDocTypes reads "durc=5,unilav=3,psc" (a bare name weighs 1).
func parseDocTypes(s string) ([]WeightedType, error) {
	var out []WeightedType
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, weight, hasWeight := strings.Cut(part, "=")
		w := 1.0
		if hasWeight {
			v, err := strconv.ParseFloat(weight, 64)
			if err != nil || v <= 0 {
				return nil, fmt.Errorf("-doc-types: bad weight in %q", part)
			}
			w = v
		}
		if !docTypeName.MatchString(name) {
			return nil, fmt.Errorf("-doc-types: bad type name %q", name)
		}
		out = append(out, WeightedType{Name: name, Weight: w})
	}
	return out, nil
}

func mustLoadConfig() Config {
	cfg, err := LoadConfig(os.Args[1:], os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mock-llm: config:", err)
		os.Exit(2)
	}
	return cfg
}
