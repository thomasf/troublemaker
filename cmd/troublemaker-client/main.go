package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/peterbourgon/ff/v3/ffcli"
)

// StatusStep defines one phase of a status error pattern simulation.
type StatusStep struct {
	Codes    []int
	Duration time.Duration
}

// SlowStep defines one phase of a slow response pattern simulation.
type SlowStep struct {
	ResponseDuration time.Duration
	StepDuration     time.Duration
}

var statusPlans = map[string][]StatusStep{
	"normal": {
		{Codes: []int{200}, Duration: 10 * time.Minute},
	},
	"degraded": {
		{Codes: []int{200}, Duration: 3 * time.Minute},
		{Codes: []int{200, 200, 200, 500}, Duration: 10 * time.Minute},
		{Codes: []int{200}, Duration: 3 * time.Minute},
	},
	"error-burst": {
		{Codes: []int{200}, Duration: 5 * time.Minute},
		{Codes: []int{500, 503}, Duration: 90 * time.Second},
		{Codes: []int{200}, Duration: 5 * time.Minute},
		{Codes: []int{200, 200, 500, 503}, Duration: 3 * time.Minute},
		{Codes: []int{200}, Duration: 5 * time.Minute},
	},
	"flapping": {
		{Codes: []int{200}, Duration: 30 * time.Second},
		{Codes: []int{500, 502, 503}, Duration: 30 * time.Second},
		{Codes: []int{200}, Duration: 30 * time.Second},
		{Codes: []int{500, 502, 503}, Duration: 30 * time.Second},
		{Codes: []int{200}, Duration: 30 * time.Second},
		{Codes: []int{500, 502, 503}, Duration: 30 * time.Second},
		{Codes: []int{200}, Duration: 30 * time.Second},
		{Codes: []int{500, 502, 503}, Duration: 30 * time.Second},
		{Codes: []int{200}, Duration: 5 * time.Minute},
	},
	"progressive": {
		{Codes: []int{200}, Duration: 5 * time.Minute},
		{Codes: []int{200, 200, 200, 500}, Duration: 3 * time.Minute},
		{Codes: []int{200, 200, 500, 500}, Duration: 3 * time.Minute},
		{Codes: []int{200, 500, 500, 500}, Duration: 3 * time.Minute},
		{Codes: []int{500, 503}, Duration: 3 * time.Minute},
		{Codes: []int{200, 500, 500, 500}, Duration: 2 * time.Minute},
		{Codes: []int{200, 200, 500, 500}, Duration: 2 * time.Minute},
		{Codes: []int{200, 200, 200, 500}, Duration: 2 * time.Minute},
		{Codes: []int{200}, Duration: 5 * time.Minute},
	},
}

var slowPlans = map[string][]SlowStep{
	"steady": {
		{ResponseDuration: 10 * time.Second, StepDuration: 20 * time.Minute},
	},
	"increasing": {
		{ResponseDuration: 500 * time.Millisecond, StepDuration: 5 * time.Minute},
		{ResponseDuration: 5 * time.Second, StepDuration: 5 * time.Minute},
		{ResponseDuration: 15 * time.Second, StepDuration: 5 * time.Minute},
		{ResponseDuration: 30 * time.Second, StepDuration: 5 * time.Minute},
		{ResponseDuration: 60 * time.Second, StepDuration: 5 * time.Minute},
	},
	"spike": {
		{ResponseDuration: 1 * time.Second, StepDuration: 5 * time.Minute},
		{ResponseDuration: 45 * time.Second, StepDuration: 2 * time.Minute},
		{ResponseDuration: 1 * time.Second, StepDuration: 5 * time.Minute},
		{ResponseDuration: 90 * time.Second, StepDuration: 90 * time.Second},
		{ResponseDuration: 1 * time.Second, StepDuration: 5 * time.Minute},
	},
	"sawtooth": {
		{ResponseDuration: 5 * time.Second, StepDuration: 3 * time.Minute},
		{ResponseDuration: 10 * time.Second, StepDuration: 3 * time.Minute},
		{ResponseDuration: 20 * time.Second, StepDuration: 3 * time.Minute},
		{ResponseDuration: 40 * time.Second, StepDuration: 3 * time.Minute},
		{ResponseDuration: 5 * time.Second, StepDuration: 3 * time.Minute},
		{ResponseDuration: 10 * time.Second, StepDuration: 3 * time.Minute},
		{ResponseDuration: 20 * time.Second, StepDuration: 3 * time.Minute},
		{ResponseDuration: 40 * time.Second, StepDuration: 3 * time.Minute},
	},
}

func generateChaosStatus() []StatusStep {
	r := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0))
	// Common HTTP status codes to pick from randomly
	allCodes := []int{
		200, 200, 200, 200, 200, // weight success
		400, 401, 403, 404, 408, 409, 429,
		500, 502, 503, 504,
	}
	newStep := func() StatusStep {
		n := r.IntN(4) + 1
		codes := make([]int, n)
		for i := range n {
			codes[i] = allCodes[r.IntN(len(allCodes))]
		}
		return StatusStep{
			Codes:    codes,
			Duration: time.Duration(r.IntN(15)+1) * time.Second,
		}
	}

	var steps []StatusStep
	var total time.Duration
	for total < 10*time.Minute {
		s := newStep()
		steps = append(steps, s)
		total += s.Duration
	}
	return steps
}

func generateChaosSlow() []SlowStep {
	r := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0))
	respDurations := []time.Duration{
		500 * time.Millisecond, time.Second, 5 * time.Second,
		10 * time.Second, 30 * time.Second, 60 * time.Second,
	}
	stepDurations := []time.Duration{
		2 * time.Minute, 3 * time.Minute, 5 * time.Minute,
	}
	var steps []SlowStep
	for range 8 {
		steps = append(steps, SlowStep{
			ResponseDuration: respDurations[r.IntN(len(respDurations))],
			StepDuration:     stepDurations[r.IntN(len(stepDurations))],
		})
	}
	return steps
}

func codesToPath(codes []int) string {
	parts := make([]string, len(codes))
	for i, c := range codes {
		parts[i] = strconv.Itoa(c)
	}
	return strings.Join(parts, ",")
}

func runStatus(ctx context.Context, baseURL, pattern string, rate float64, maxConcurrency int, progress bool) error {
	if baseURL == "" {
		return fmt.Errorf("-url is required")
	}
	baseURL = strings.TrimRight(baseURL, "/")

	var steps []StatusStep
	if pattern == "chaos" {
		steps = generateChaosStatus()
	} else {
		var ok bool
		steps, ok = statusPlans[pattern]
		if !ok {
			return fmt.Errorf("unknown pattern %q, choose: normal, degraded, error-burst, flapping, progressive, chaos", pattern)
		}
	}

	client := &http.Client{Timeout: 30 * time.Second}
	semSize := maxConcurrency
	if semSize <= 0 {
		semSize = 10000
	}
	sem := make(chan struct{}, semSize)
	tickInterval := time.Duration(float64(time.Second) / rate)
	t0 := time.Now()

	fmt.Printf("status: pattern=%s steps=%d rate=%.2f/s max-concurrency=%d\n",
		pattern, len(steps), rate, semSize)

	for i, step := range steps {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		endpoint := baseURL + "/status/" + codesToPath(step.Codes)
		fmt.Printf("[%s] step %d/%d codes=%v duration=%s\n",
			time.Since(t0).Round(time.Second), i+1, len(steps), step.Codes, step.Duration)

		stepCtx, stepCancel := context.WithTimeout(ctx, step.Duration)
		tick := time.NewTicker(tickInterval)

		var success, fail, reqErrors, skipped atomic.Int64
		var wg sync.WaitGroup
		stepStart := time.Now()

	stepLoop:
		for {
			select {
			case <-tick.C:
				select {
				case sem <- struct{}{}:
					wg.Add(1)
					go func() {
						defer wg.Done()
						defer func() { <-sem }()
						if progress {
							fmt.Print(".")
						}
						req, err := http.NewRequestWithContext(stepCtx, http.MethodGet, endpoint, nil)
						if err != nil {
							reqErrors.Add(1)
							return
						}
						resp, err := client.Do(req)
						if err != nil {
							if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
								reqErrors.Add(1)
							}
							return
						}
						resp.Body.Close()
						if resp.StatusCode >= 200 && resp.StatusCode < 300 {
							success.Add(1)
						} else {
							fail.Add(1)
						}
					}()
				default:
					skipped.Add(1)
				}
			case <-stepCtx.Done():
				break stepLoop
			}
		}

		tick.Stop()
		stepCancel()
		wg.Wait()
		if progress {
			fmt.Println()
		}
		elapsed := time.Since(stepStart).Seconds()
		sent := success.Load() + fail.Load()
		actualRPS := float64(sent) / elapsed
		fmt.Printf("  done: success=%d non-2xx=%d errors=%d skipped=%d actual-rps=%.2f\n",
			success.Load(), fail.Load(), reqErrors.Load(), skipped.Load(), actualRPS)
	}
	return nil
}

func runSlow(ctx context.Context, baseURL, pattern string, rate float64, maxConcurrency int, progress bool) error {
	if baseURL == "" {
		return fmt.Errorf("-url is required")
	}
	baseURL = strings.TrimRight(baseURL, "/")

	var steps []SlowStep
	if pattern == "chaos" {
		steps = generateChaosSlow()
	} else {
		var ok bool
		steps, ok = slowPlans[pattern]
		if !ok {
			return fmt.Errorf("unknown pattern %q, choose: steady, increasing, spike, sawtooth, chaos", pattern)
		}
	}

	// Semaphore shared across steps so in-flight requests from one step count
	// against the limit of the next.
	semSize := maxConcurrency
	if semSize <= 0 {
		semSize = 10000
	}
	sem := make(chan struct{}, semSize)
	tickInterval := time.Duration(float64(time.Second) / rate)
	t0 := time.Now()

	fmt.Printf("slow: pattern=%s steps=%d rate=%.2f/s max-concurrency=%d\n", pattern, len(steps), rate, semSize)

	var wg sync.WaitGroup

	for i, step := range steps {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		fmt.Printf("[%s] step %d/%d response-duration=%s step-duration=%s\n",
			time.Since(t0).Round(time.Second), i+1, len(steps),
			step.ResponseDuration, step.StepDuration)

		endpoint := fmt.Sprintf("%s/slow?duration=%s&interval=1s", baseURL, step.ResponseDuration)
		client := &http.Client{Timeout: step.ResponseDuration + 30*time.Second}

		stepCtx, stepCancel := context.WithTimeout(ctx, step.StepDuration)
		tick := time.NewTicker(tickInterval)

		var completed, reqErrors, skipped atomic.Int64
		stepStart := time.Now()

	stepLoop:
		for {
			select {
			case <-tick.C:
				select {
				case sem <- struct{}{}:
					wg.Add(1)
					go func() {
						defer wg.Done()
						defer func() { <-sem }()
						// Use outer ctx so in-flight requests survive step transitions.
						if progress {
							fmt.Print(".")
						}
						req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
						if err != nil {
							reqErrors.Add(1)
							return
						}
						resp, err := client.Do(req)
						if err != nil {
							if !errors.Is(err, context.Canceled) {
								reqErrors.Add(1)
							}
							return
						}
						resp.Body.Close()
						completed.Add(1)
					}()
				default:
					skipped.Add(1)
				}
			case <-stepCtx.Done():
				break stepLoop
			}
		}

		tick.Stop()
		stepCancel()
		if progress {
			fmt.Println()
		}
		elapsed := time.Since(stepStart).Seconds()
		actualRPS := float64(completed.Load()) / elapsed
		fmt.Printf("  step ended: completed=%d errors=%d skipped=%d actual-rps=%.2f\n",
			completed.Load(), reqErrors.Load(), skipped.Load(), actualRPS)
	}

	// Wait for all in-flight requests to drain.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}

	return nil
}

func main() {
	rootFS := flag.NewFlagSet("troublemaker-client", flag.ExitOnError)
	rootURL := rootFS.String("url", "", "troublemaker instance URL (required)")
	rootProgress := rootFS.Bool("progress", false, "print a dot to stdout on each request")

	statusFS := flag.NewFlagSet("status", flag.ExitOnError)
	statusPattern := statusFS.String("pattern", "chaos", "error pattern: normal, degraded, error-burst, flapping, progressive, chaos")
	statusRate := statusFS.Float64("rate", 1.0, "target requests per second")
	statusMaxConcurrency := statusFS.Int("max-concurrency", 10, "max concurrent in-flight requests (0=unlimited)")

	statusCmd := &ffcli.Command{
		Name:       "status",
		ShortUsage: "troublemaker-client -url <url> [flags] status [flags]",
		ShortHelp:  "simulate HTTP error patterns using the /status endpoint",
		FlagSet:    statusFS,
		Exec: func(ctx context.Context, args []string) error {
			return runStatus(ctx, *rootURL, *statusPattern, *statusRate, *statusMaxConcurrency, *rootProgress)
		},
	}

	slowFS := flag.NewFlagSet("slow", flag.ExitOnError)
	slowPattern := slowFS.String("pattern", "spike", "slow pattern: steady, increasing, spike, sawtooth, chaos")
	slowRate := slowFS.Float64("rate", 0.5, "target requests per second")
	slowMaxConcurrency := slowFS.Int("max-concurrency", 50, "max concurrent in-flight requests (0=unlimited)")

	slowCmd := &ffcli.Command{
		Name:       "slow",
		ShortUsage: "troublemaker-client -url <url> [flags] slow [flags]",
		ShortHelp:  "simulate varying response durations using the /slow endpoint",
		FlagSet:    slowFS,
		Exec: func(ctx context.Context, args []string) error {
			return runSlow(ctx, *rootURL, *slowPattern, *slowRate, *slowMaxConcurrency, *rootProgress)
		},
	}

	root := &ffcli.Command{
		ShortUsage:  "troublemaker-client -url <url> [flags] <subcommand> [flags]",
		ShortHelp:   "generate usage patterns against a troublemaker instance",
		FlagSet:     rootFS,
		Subcommands: []*ffcli.Command{statusCmd, slowCmd},
		Exec: func(ctx context.Context, args []string) error {
			return flag.ErrHelp
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := root.ParseAndRun(ctx, os.Args[1:]); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
