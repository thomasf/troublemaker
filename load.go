package main

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/xid"
	"github.com/rs/zerolog"
)

const (
	RandomLoadTotalDuration = 40 * time.Minute // Total test duration
)

type LoadStep struct {
	CPUPercent int           `json:"cpu_percent"`
	MemMB      int           `json:"mem_mb"`
	Duration   time.Duration `json:"duration"`
}

type LoadGenerator struct {
	isRunning atomic.Int32
	mu        sync.RWMutex
	cancel    context.CancelFunc
	steps     []LoadStep
	stepIdx   int
	startTime time.Time
	stepStart time.Time
	logger    zerolog.Logger

	CPUMax int
	MemMax int
}

func NewLoadGenerator(logger zerolog.Logger) *LoadGenerator {
	return &LoadGenerator{
		logger: logger,
		CPUMax: 80,
		MemMax: 512,
	}
}

func (lg *LoadGenerator) IsRunning() bool {
	return lg.isRunning.Load() == 1
}

func (lg *LoadGenerator) GetStatus() (bool, []LoadStep, int, time.Time, time.Time) {
	lg.mu.RLock()
	defer lg.mu.RUnlock()
	return lg.IsRunning(), lg.steps, lg.stepIdx, lg.startTime, lg.stepStart
}

func (lg *LoadGenerator) Abort() bool {
	lg.mu.Lock()
	defer lg.mu.Unlock()
	if lg.cancel != nil {
		lg.cancel()
		lg.cancel = nil
		return true
	}
	return false
}

func (lg *LoadGenerator) StartSchedule(seed uint64, duration time.Duration, types string) ([]LoadStep, bool) {
	steps := lg.GenerateRandomSchedule(seed, duration, types)

	started := make(chan bool)
	go func() {
		ok := lg.guard(func(ctx context.Context) {
			started <- true
			lg.runLoadSteps(ctx, steps)
		})
		if !ok {
			started <- false
		}
	}()

	return steps, <-started
}

func (lg *LoadGenerator) StartCPULoad() {
	lg.guard(lg.doStartCPULoad)
}

func (lg *LoadGenerator) StartMemLoad() {
	lg.guard(lg.doStartMemLoad)
}

func (lg *LoadGenerator) StartCombinedLoad() {
	lg.guard(lg.doStartCombinedLoad)
}

func (lg *LoadGenerator) StartSineLoad() {
	lg.guard(lg.doStartSineLoad)
}

func (lg *LoadGenerator) StartSpikeLoad() {
	lg.guard(lg.doStartSpikeLoad)
}

func (lg *LoadGenerator) guard(fn func(ctx context.Context)) bool {
	if !lg.isRunning.CompareAndSwap(0, 1) {
		lg.logger.Warn().Msg("a load generator is already running, skipping")
		return false
	}
	defer lg.isRunning.Store(0)

	var ctx context.Context
	ctx, cancel := context.WithCancel(context.Background())
	lg.mu.Lock()
	lg.cancel = cancel
	lg.mu.Unlock()

	defer func() {
		lg.mu.Lock()
		defer lg.mu.Unlock()
		if lg.cancel != nil {
			lg.cancel()
		}
		lg.cancel = nil
		lg.steps = nil
	}()

	fn(ctx)
	return true
}

func (lg *LoadGenerator) runLoadSteps(ctx context.Context, steps []LoadStep) {
	lg.mu.Lock()
	lg.steps = steps
	lg.startTime = time.Now()
	lg.mu.Unlock()

	var data []byte
	t0 := time.Now()
	for i, step := range steps {
		select {
		case <-ctx.Done():
			return
		default:
		}

		lg.mu.Lock()
		lg.stepIdx = i
		lg.stepStart = time.Now()
		lg.mu.Unlock()

		logger := lg.logger.With().
			Int("step.#", i).
			Dur("elapsed", time.Since(t0).Round(100*time.Millisecond)).
			Logger()

		logger.Info().
			Int("cpu", step.CPUPercent).
			Int("mem", step.MemMB).
			Dur("dur", step.Duration).
			Msg("step starts")

		// Clear old data first if it exists to avoid memory peaks during re-allocation
		if data != nil {
			data = nil
			runtime.GC()
			debug.FreeOSMemory()
		}

		if step.MemMB > 0 {
			newData := make([]byte, step.MemMB*1024*1024)
			for j := 0; j < len(newData); j += 4096 {
				newData[j] = 1
			}
			data = newData
		}

		if step.CPUPercent > 0 {
			lg.doBusyWork(ctx, step.Duration, step.CPUPercent)
		} else {
			t := time.NewTimer(step.Duration)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return
			}
		}
	}
}

func (lg *LoadGenerator) GenerateRandomSchedule(seed uint64, duration time.Duration, types string) []LoadStep {
	r := rand.New(rand.NewPCG(seed, seed))
	var steps []LoadStep
	totalDur := time.Duration(0)

	if duration <= 0 {
		duration = RandomLoadTotalDuration
	}
	hasCPU := strings.Contains(types, "cpu")
	hasMem := strings.Contains(types, "mem")

	type Phase int
	const (
		None Phase = iota
		SustainedLoad
		VaryingLoad
		Idle
	)

	var prevPhase Phase
	for totalDur < duration {
		var currentPhase Phase
		if prevPhase == None || prevPhase == Idle {
			if r.Float64() < 0.5 {
				currentPhase = SustainedLoad
			} else {
				currentPhase = VaryingLoad
			}
		} else {
			if r.Float64() < 0.5 {
				currentPhase = Idle
			} else {
				if prevPhase == SustainedLoad {
					currentPhase = VaryingLoad
				} else {
					currentPhase = SustainedLoad
				}
			}
		}
		prevPhase = currentPhase

		phaseDuration := time.Duration(5+r.IntN(10)) * time.Minute

		if totalDur+phaseDuration > duration {
			phaseDuration = duration - totalDur
		}

		switch currentPhase {
		case SustainedLoad:
			cpu := 0
			if hasCPU {
				cpu = 30 + r.IntN(max(1, lg.CPUMax-30))
			}
			mem := 0
			if hasMem {
				mem = r.IntN(lg.MemMax + 1)
			}
			steps = append(steps, LoadStep{
				CPUPercent: cpu,
				MemMB:      mem,
				Duration:   phaseDuration,
			})
			totalDur += phaseDuration

		case VaryingLoad:
			phaseEnd := totalDur + phaseDuration
			for totalDur < phaseEnd {
				stepDur := time.Duration(2+r.IntN(28)) * time.Second
				if totalDur+stepDur > phaseEnd {
					stepDur = phaseEnd - totalDur
				}
				cpu := 0
				if hasCPU {
					cpu = r.IntN(lg.CPUMax + 1)
				}
				mem := 0
				if hasMem {
					mem = r.IntN(lg.MemMax + 1)
				}
				steps = append(steps, LoadStep{
					CPUPercent: cpu,
					MemMB:      mem,
					Duration:   stepDur,
				})
				totalDur += stepDur
			}

		case Idle:
			steps = append(steps, LoadStep{
				Duration: phaseDuration,
			})
			totalDur += phaseDuration
		}
	}
	return steps
}

func (lg *LoadGenerator) doStartCPULoad(ctx context.Context) {
	testID := xid.New()
	logger := lg.logger.With().Str("cpuload.id", testID.String()).Logger()
	logger.Info().Msg("load test starts")
	defer logger.Info().Msg("load test ended")

	const normal = 6 * time.Minute
	const burst = 30 * time.Second
	const sleep = 6 * time.Minute
	const shortSleep = 30 * time.Second
	const longSleep = 10 * time.Minute

	cp := func(percent int) int {
		return int(float64(percent) / 90.0 * float64(lg.CPUMax))
	}

	tests := []LoadStep{
		{Duration: burst, CPUPercent: cp(90)},
		{Duration: shortSleep},
		{Duration: burst, CPUPercent: cp(90)},
		{Duration: shortSleep},
		{Duration: normal, CPUPercent: cp(10)},
		{Duration: sleep},
		{Duration: normal, CPUPercent: cp(20)},
		{Duration: sleep},
		{Duration: normal, CPUPercent: cp(30)},
		{Duration: sleep},
		{Duration: normal, CPUPercent: cp(40)},
		{Duration: sleep},
		{Duration: normal, CPUPercent: cp(50)},
		{Duration: sleep},
		{Duration: normal, CPUPercent: cp(60)},
		{Duration: sleep},
		{Duration: normal, CPUPercent: cp(70)},
		{Duration: sleep},
		{Duration: burst, CPUPercent: cp(90)},
		{Duration: shortSleep},
		{Duration: burst, CPUPercent: cp(90)},
		{Duration: sleep},
		{Duration: normal, CPUPercent: cp(70)},
		{Duration: normal, CPUPercent: cp(50)},
		{Duration: normal, CPUPercent: cp(20)},
		{Duration: shortSleep},
		{Duration: burst, CPUPercent: cp(90)},
		{Duration: longSleep},
		{Duration: burst, CPUPercent: cp(90)},
		{Duration: longSleep},
		{Duration: burst, CPUPercent: cp(90)},
		{Duration: sleep},
		{Duration: burst, CPUPercent: cp(50)},
		{Duration: sleep},
		{Duration: burst, CPUPercent: cp(80)},
		{Duration: sleep},
		{Duration: burst, CPUPercent: cp(70)},
		{Duration: longSleep},
	}

	lg.runLoadSteps(ctx, tests)
}

func (lg *LoadGenerator) doStartMemLoad(ctx context.Context) {
	testID := xid.New()
	logger := lg.logger.With().Str("memload.id", testID.String()).Logger()
	logger.Info().Msg("memload test starts")
	defer logger.Info().Msg("memload test ended")

	const short = 1 * time.Minute
	const long = 5 * time.Minute

	mp := func(m int) int {
		return int(float64(min(100, m)) / 100.0 * float64(lg.MemMax))
	}

	tests := []LoadStep{
		{Duration: short, MemMB: mp(25)},
		{Duration: short, MemMB: 0},
		{Duration: short, MemMB: mp(75)},
		{Duration: short, MemMB: 0},
		{Duration: long, MemMB: mp(75)},
		{Duration: short, MemMB: 0},
		{Duration: long, MemMB: mp(100)},
		{Duration: short, MemMB: 0},
	}

	lg.runLoadSteps(ctx, tests)
}

func (lg *LoadGenerator) doStartCombinedLoad(ctx context.Context) {
	testID := xid.New()
	logger := lg.logger.With().Str("combinedload.id", testID.String()).Logger()
	logger.Info().Msg("combined load test starts")
	defer logger.Info().Msg("combined load test ended")

	mp := func(m int) int {
		return int(float64(min(100, m)) / 100.0 * float64(lg.MemMax))
	}
	cp := func(c int) int {
		return int(float64(min(100, c)) / 100.0 * float64(lg.CPUMax))
	}

	tests := []LoadStep{
		{Duration: 1 * time.Minute, CPUPercent: cp(50), MemMB: mp(50)},
		{Duration: 1 * time.Minute, CPUPercent: cp(10), MemMB: mp(100)},
		{Duration: 1 * time.Minute, CPUPercent: cp(90), MemMB: mp(25)},
		{Duration: 1 * time.Minute, CPUPercent: 0, MemMB: 0},
	}

	lg.runLoadSteps(ctx, tests)
}

func (lg *LoadGenerator) doStartSineLoad(ctx context.Context) {
	testID := xid.New()
	logger := lg.logger.With().Str("sineload.id", testID.String()).Logger()
	logger.Info().Msg("sine wave load test starts")
	defer logger.Info().Msg("sine wave load test ended")
	const totalDuration = time.Hour
	const cycleDuration = 10 * time.Minute
	const stepDuration = 4 * time.Second
	const numSteps = int(totalDuration / stepDuration)

	steps := make([]LoadStep, numSteps)
	for i := range numSteps {
		x := 2 * math.Pi * float64(time.Duration(i)*stepDuration) / float64(cycleDuration)
		cpuSine := (math.Sin(x) + 1) / 2
		cpu := int(cpuSine * float64(lg.CPUMax))
		memSine := (math.Sin(x+math.Pi) + 1) / 2
		mem := int(memSine * float64(lg.MemMax))
		steps[i] = LoadStep{
			Duration:   stepDuration,
			CPUPercent: cpu,
			MemMB:      mem,
		}
	}
	lg.runLoadSteps(ctx, steps)
}

func (lg *LoadGenerator) doStartSpikeLoad(ctx context.Context) {
	testID := xid.New()
	logger := lg.logger.With().Str("spikeload.id", testID.String()).Logger()
	logger.Info().Msg("spike load test starts")
	defer logger.Info().Msg("spike load test ended")

	r := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), uint64(time.Now().UnixNano())))
	const totalDuration = time.Hour
	var steps []LoadStep
	var currentDur time.Duration

	for currentDur < totalDuration {
		// Idle period: 1 to 5 minutes
		idleDur := time.Duration(60+r.IntN(240)) * time.Second
		if currentDur+idleDur > totalDuration {
			idleDur = totalDuration - currentDur
		}
		steps = append(steps, LoadStep{Duration: idleDur})
		currentDur += idleDur

		if currentDur >= totalDuration {
			break
		}

		// Spike period: 5 to 15 seconds
		spikeDur := time.Duration(5+r.IntN(11)) * time.Second
		if currentDur+spikeDur > totalDuration {
			spikeDur = totalDuration - currentDur
		}
		cpu := 80 + r.IntN(21) // 80-100%
		cpu = min(cpu, lg.CPUMax)
		steps = append(steps, LoadStep{Duration: spikeDur, CPUPercent: cpu})
		currentDur += spikeDur
	}

	lg.runLoadSteps(ctx, steps)
}

func (lg *LoadGenerator) doBusyWork(ctx context.Context, duration time.Duration, percentage int) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	percentage = max(0, min(100, percentage))
	unitCycle := 100 * time.Millisecond
	workTime := time.Duration(percentage) * unitCycle / 100
	sleepTime := unitCycle - workTime
	endTime := time.Now().Add(duration)
	for time.Now().Before(endTime) {
		select {
		case <-ctx.Done():
			return
		default:
		}
		startWork := time.Now()
		for time.Since(startWork) < workTime {
			// consume CPU
		}
		if sleepTime > 0 {
			t := time.NewTimer(sleepTime)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return
			}
		}
	}
}

func (lg *LoadGenerator) StatusHandler(w http.ResponseWriter, r *http.Request) {
	isRunning, steps, stepIdx, startTime, stepStart := lg.GetStatus()

	if !isRunning || len(steps) == 0 {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, "No load test is currently running.")
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintln(w, "Currently Running Load Test Schedule")
	fmt.Fprintf(w, "Started at: %s (%s ago)\n", startTime.Format("15:04:05"), time.Since(startTime).Round(time.Second))
	fmt.Fprintln(w, "--------------------------------------------------------------------------------")
	fmt.Fprintf(w, "%-4s | %-12s | %-20s | %-10s | %-5s | %-5s | %s\n", "Step", "Relative", "Wall Clock", "Duration", "CPU%", "MemMB", "Status")

	relTime := time.Duration(0)
	for i, step := range steps {
		status := ""
		if i < stepIdx {
			status = "Completed"
		} else if i == stepIdx {
			status = fmt.Sprintf("RUNNING (%s elapsed)", time.Since(stepStart).Round(time.Second))
		} else {
			status = "Pending"
		}

		fmt.Fprintf(w, "%-4d | %-12s | %-20s | %-10s | %-5d | %-5d | %s\n",
			i, relTime, startTime.Add(relTime).Format("15:04:05"), step.Duration, step.CPUPercent, step.MemMB, status)
		relTime += step.Duration
	}
	fmt.Fprintln(w, "--------------------------------------------------------------------------------")
}

func (lg *LoadGenerator) AbortHandler(w http.ResponseWriter, r *http.Request) {
	if lg.Abort() {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "Load test aborted")
	} else {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprintln(w, "No load test running")
	}
}

func (lg *LoadGenerator) CPULoadHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	go lg.StartCPULoad()
}

func (lg *LoadGenerator) MemLoadHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	go lg.StartMemLoad()
}

func (lg *LoadGenerator) CombinedLoadHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	go lg.StartCombinedLoad()
}

func (lg *LoadGenerator) SineLoadHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	go lg.StartSineLoad()
}

func (lg *LoadGenerator) SpikeLoadHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	go lg.StartSpikeLoad()
}

func (lg *LoadGenerator) RandomLoadHandler(w http.ResponseWriter, r *http.Request) {
	seed := rand.Uint64()
	target := fmt.Sprintf("/load/seed/%d", seed)
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (lg *LoadGenerator) SeedLoadHandler(w http.ResponseWriter, r *http.Request) {
	seedStr := strings.TrimPrefix(r.URL.Path, "/load/seed/")
	if seedStr == "" {
		target := "/load/random"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusFound)
		return
	}
	seed, err := strconv.ParseUint(seedStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid seed", http.StatusBadRequest)
		return
	}

	duration := RandomLoadTotalDuration
	if dStr := r.URL.Query().Get("duration"); dStr != "" {
		if d, err := time.ParseDuration(dStr); err == nil {
			duration = d
		}
	}
	types := r.URL.Query().Get("types")
	if types == "" {
		types = "cpu,mem"
	}

	steps, ok := lg.StartSchedule(seed, duration, types)

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	if !ok {
		fmt.Fprintln(w, "\nWARNING: A load generator is already running. This schedule was NOT started.")
		fmt.Fprintln(w, "--------------------------------------------------")
	}
	fmt.Fprintf(w, "Random Load Schedule (Seed: %d)\n", seed)
	fmt.Fprintln(w, "--------------------------------------------------")
	fmt.Fprintf(w, "%-4s | %-12s | %-20s | %-10s | %-5s | %-5s\n", "Step", "Relative", "Wall Clock", "Duration", "CPU%", "MemMB")

	now := time.Now()
	relTime := time.Duration(0)
	for i, step := range steps {
		fmt.Fprintf(w, "%-4d | %-12s | %-20s | %-10s | %-5d | %-5d\n",
			i, relTime, now.Add(relTime).Format("15:04:05"), step.Duration, step.CPUPercent, step.MemMB)
		relTime += step.Duration
	}
	fmt.Fprintln(w, "--------------------------------------------------")
}
