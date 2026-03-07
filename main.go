package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/peterbourgon/ff/v3"
	"github.com/rs/xid"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	DefaultExitCode        = 1
	DefaultSlowDuration    = 5 * time.Minute
	DefaultSlowInterval    = 100 * time.Millisecond
	DefaultNothingDuration = 5 * time.Minute
)

var logger zerolog.Logger

var (
	startTime = time.Now().UTC()
	t0        = time.Now()
)

var instanceID = xid.New().String()

type LogBuffer struct {
	mu    sync.RWMutex
	lines []string
	size  int
}

func (lb *LogBuffer) Write(p []byte) (n int, err error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	if lb.size <= 0 {
		return len(p), nil
	}
	lb.lines = append(lb.lines, string(p))
	if len(lb.lines) > lb.size {
		lb.lines = lb.lines[len(lb.lines)-lb.size:]
	}
	return len(p), nil
}

func (lb *LogBuffer) WriteTo(w io.Writer) (int64, error) {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	var total int64
	for _, line := range lb.lines {
		n, err := fmt.Fprint(w, line)
		total += int64(n)
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

var logBuffer = &LogBuffer{}

func init() {
	consoleWriter := zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: time.RFC3339Nano,
	}

	bufferWriter := zerolog.ConsoleWriter{
		Out:        logBuffer,
		TimeFormat: time.RFC3339Nano,
		NoColor:    true,
	}

	multi := zerolog.MultiLevelWriter(consoleWriter, bufferWriter)

	log.Logger = log.Output(multi)

	logger = log.With().
		Str("instance", instanceID).
		Logger().
		Hook(zerolog.HookFunc(func(e *zerolog.Event, level zerolog.Level, message string) {
			e.Str("t", time.Now().Sub(t0).String())
		}))
}

func newLogsHandler() func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		logBuffer.WriteTo(w)
	}
}

type responseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (rw *responseWriter) WriteHeader(code int) {
	if rw.wroteHeader {
		return
	}
	rw.status = code
	rw.wroteHeader = true
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(p []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.ResponseWriter.Write(p)
}

func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

var logIgnored = map[string]bool{
	"/logs":        true,
	"/favicon.ico": true,
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		if logIgnored[r.URL.Path] {
			return
		}
		logger.Info().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", rw.status).
			Dur("duration", time.Since(start)).
			Str("remote_addr", r.RemoteAddr).
			Str("user_agent", r.UserAgent()).
			Msg("http request")
	})
}

func newInfoHandler(flags Flags, effectiveSettings EffectiveSettings) func(w http.ResponseWriter, r *http.Request) {
	flagsData, err := json.MarshalIndent(flags, "", "  ")
	if err != nil {
		panic(err)
	}
	effectiveSettingsData, err := json.MarshalIndent(effectiveSettings, "", "  ")
	if err != nil {
		panic(err)
	}
	buildInfoStr := ""
	buildInfo, ok := debug.ReadBuildInfo()
	if ok {
		buildInfoStr = buildInfo.String()
	}

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "\nSystem info:")
		fmt.Fprintf(w, "numcpu: %v\n", runtime.NumCPU())
		fmt.Fprintf(w, "maxprocs: %v\n", runtime.GOMAXPROCS(0))
		fmt.Fprintf(w, "instance id: %s\n", instanceID)
		fmt.Fprintf(w, "time: %s\n", time.Now().Format(time.RFC3339Nano))
		fmt.Fprintf(w, "uptime %s\n", time.Now().Sub(t0).String())

		fmt.Fprintf(w, "\nBuild info:\n%s\n", buildInfoStr)
		fmt.Fprintf(w, "\nFlags:\n%s\n", string(flagsData))
		fmt.Fprintf(w, "\nEffective settings:\n%s", string(effectiveSettingsData))
	}
}

func newRootHandler() func(w http.ResponseWriter, r *http.Request) {
	var version string
	buildInfo, ok := debug.ReadBuildInfo()
	if ok {
		version = buildInfo.Main.Version
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "troublemaker: %s\n", version)
		fmt.Fprintf(w, "instance id: %s\n", instanceID)
		fmt.Fprintf(w, "time: %s\n", time.Now().Format(time.RFC3339Nano))
		fmt.Fprintf(w, "uptime %s\n", time.Now().Sub(t0).String())
	}
}

//go:embed docs.html
var docsData []byte

func newDocsHandler(flags Flags, effective EffectiveSettings, usage string) func(w http.ResponseWriter, r *http.Request) {
	flagsData, _ := json.MarshalIndent(flags, "", "  ")
	effectiveData, _ := json.MarshalIndent(effective, "", "  ")

	buildInfoStr := ""
	buildInfo, ok := debug.ReadBuildInfo()
	if ok {
		buildInfoStr = buildInfo.String()
	}

	tmpl, err := template.New("docs").Parse(string(docsData))
	if err != nil {
		panic(err)
	}

	var buf bytes.Buffer
	data := struct {
		Flags                  string
		EffectiveSettings      string
		Usage                  string
		BuildInfo              string
		DefaultExitCode        int
		DefaultSlowDuration    time.Duration
		DefaultSlowInterval    time.Duration
		DefaultNothingDuration time.Duration
	}{
		Flags:                  string(flagsData),
		EffectiveSettings:      string(effectiveData),
		Usage:                  usage,
		BuildInfo:              buildInfoStr,
		DefaultExitCode:        DefaultExitCode,
		DefaultSlowDuration:    DefaultSlowDuration,
		DefaultSlowInterval:    DefaultSlowInterval,
		DefaultNothingDuration: DefaultNothingDuration,
	}
	if err := tmpl.Execute(&buf, data); err != nil {
		panic(err)
	}
	renderedDocs := buf.Bytes()

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write(renderedDocs)
	}
}

// Flags .
type Flags struct {
	WebEnable       bool          `json:"web.enable"`
	WebListen       string        `json:"web.listen"`
	WebDelay        time.Duration `json:"web.delay"`
	WebDelayJitter  time.Duration `json:"web.delay.jitter"`
	ExitAfter       time.Duration `json:"exit.after"`
	ExitAfterJitter time.Duration `json:"exit.after.jitter"`
	ExitPercent     int           `json:"exit.percent"`
	ExitCode        int           `json:"exit.code"`
	IgnoreSignals   bool          `json:"ignore.signals"`
	CPUloadEnable   bool          `json:"cpuload.enable"`
	CPULoadWorkers  int           `json:"cpuload.workers"`
	CPULoadDuration time.Duration `json:"cpuload.duration"`

	MemloadEnable bool          `json:"memload.enable"`
	MemloadMB     int           `json:"memload.mb"`
	MemloadWait   time.Duration `json:"memload.wait"`

	LogSize int `json:"log.size"`

	RandSeed1 uint64 `json:"rand.seed1"`
	RandSeed2 uint64 `json:"rand.seed2"`
}

func (f *Flags) Register(fs *flag.FlagSet) {
	fs.BoolVar(&f.WebEnable, "web.enable", true, "enable http server")
	fs.StringVar(&f.WebListen, "web.listen", "0.0.0.0:8092", "http server bind addr")
	fs.DurationVar(&f.WebDelay, "web.delay", 0, "sleep duration before starting http server")
	fs.DurationVar(&f.WebDelayJitter, "web.delay.jitter", 0, "delay +/- jitter")

	fs.DurationVar(&f.ExitAfter, "exit.after", 0, "exit with exit code 1 if duration > 0, 1ns=exit asap")
	fs.DurationVar(&f.ExitAfterJitter, "exit.after.jitter", 0, "exit after +/- jitter")
	fs.IntVar(&f.ExitPercent, "exit.percent", 100, "% chance to exit if exit.after is set")

	fs.IntVar(&f.ExitCode, "exit.code", DefaultExitCode, "exit code when exiting")

	fs.BoolVar(&f.IgnoreSignals, "signals.ignore", false, "ignore shutdown signals")

	fs.BoolVar(&f.CPUloadEnable, "cpuload.enable", false, "enable cpu load generator")
	fs.IntVar(&f.CPULoadWorkers, "cpuload.workers", 1, "number of concurrent goroutines, won't go over max")

	fs.BoolVar(&f.MemloadEnable, "memload.enable", false, "enable memory load generator")
	fs.IntVar(&f.MemloadMB, "memload.mb", 100, "memory to allocate in MB")
	fs.DurationVar(&f.MemloadWait, "memload.wait", 0, "wait duration before starting memory load")

	fs.IntVar(&f.LogSize, "log.size", 10000, "number of log lines to keep in memory")

	fs.Uint64Var(&f.RandSeed1, "rand.seed1", rand.Uint64(), "seed1 for random generator")
	fs.Uint64Var(&f.RandSeed2, "rand.seed2", rand.Uint64(), "seed2 for random generator")
}

func (f Flags) EffectiveSettings() EffectiveSettings {
	r := rand.New(rand.NewPCG(f.RandSeed1, f.RandSeed2))

	effectiveDuration := func(delay, jitter time.Duration) time.Duration {
		if jitter == 0 || delay < 2 {
			return delay
		}
		v := delay + time.Duration(r.Int64N(2*int64(jitter))) - time.Duration(int64(jitter))

		return max(0, v)
	}

	return EffectiveSettings{
		ExitAfter:  effectiveDuration(f.ExitAfter, f.ExitAfterJitter),
		WebDelay:   effectiveDuration(f.WebDelay, f.WebDelayJitter),
		ShouldExit: f.ExitAfter > 0 && (f.ExitPercent == 100 || float64(f.ExitPercent)/100 >= r.Float64()),
	}
}

type EffectiveSettings struct {
	ExitAfter  time.Duration `json:"exit.after"`
	WebDelay   time.Duration `json:"web.delay"`
	ShouldExit bool          `json:"should exit"`
}

func main() {
	logger.Info().Time("t0", startTime).Msg("started")
	var flags Flags

	fs := flag.NewFlagSet("troublemaker", flag.ContinueOnError)
	flags.Register(fs)

	var usageBuf bytes.Buffer
	fs.SetOutput(&usageBuf)
	fs.Usage()
	usage := usageBuf.String()

	if err := ff.Parse(fs, os.Args[1:],
		ff.WithEnvVarNoPrefix(),
		ff.WithConfigFileFlag("config"),
		ff.WithConfigFileParser(ff.PlainParser),
	); err != nil {
		logger.Err(err).Msg("could not parse flags")
		os.Exit(1)
	}

	logBuffer.mu.Lock()
	logBuffer.size = flags.LogSize
	logBuffer.mu.Unlock()

	logger.Info().Str("command", os.Args[0]).Strs("args", fs.Args()).Msg("command line args")

	if flags.IgnoreSignals {
		c := make(chan os.Signal, 1)
		signal.Notify(c,
			syscall.SIGABRT,
			syscall.SIGHUP,
			syscall.SIGINT,
			syscall.SIGPIPE,
			syscall.SIGTERM,
		)
		go func() {
			for s := range c {
				logger.Info().Stringer("signal", s).
					Dur("after", time.Now().Sub(startTime)).
					Msg("ignore signal")
			}
		}()
	}

	effectiveSettings := flags.EffectiveSettings()

	logger.Info().Interface("data", &flags).Msg("flags")
	logger.Info().Interface("data", &effectiveSettings).Msg("effective settings")

	if fs.NArg() > 0 {
		switch fs.Arg(0) {
		case "sleep":
			d, err := time.ParseDuration(fs.Arg(1))
			if err != nil {
				logger.Fatal().Err(err).Msg("sleep requires duration")
			}
			time.Sleep(d)
			os.Exit(flags.ExitCode)
		case "beat":
			log.Info().Msg("start as beat")
		default:
			fmt.Println("unknown subcommand:", fs.Arg(0))
			os.Exit(1)
		}
	}

	if effectiveSettings.ShouldExit {
		if effectiveSettings.ExitAfter == time.Nanosecond {
			logger.Info().Msg("exit at startup")
			os.Exit(flags.ExitCode)
		} else {
			go func() {
				time.Sleep(effectiveSettings.ExitAfter)
				logger.Info().Msg("exit after sleep")
				os.Exit(flags.ExitCode)
			}()
		}
	}

	if flags.WebEnable {
		go func() {
			mux := http.NewServeMux()
			mux.HandleFunc("/", newRootHandler())
			mux.HandleFunc("/docs", newDocsHandler(flags, effectiveSettings, usage))
			mux.HandleFunc("/info", newInfoHandler(flags, effectiveSettings))
			mux.HandleFunc("/logs", newLogsHandler())

			mux.HandleFunc("/load/cpu", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				startCPULoad()
			})
			mux.HandleFunc("/load/mem", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				startMemLoad()
			})
			mux.HandleFunc("/load/combined", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				startCombinedLoad()
			})
			mux.HandleFunc("/exit/", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				codeStr := r.URL.Query().Get("code")
				code, err := strconv.ParseInt(codeStr, 10, 64)
				if err != nil || code < 0 || code > 127 {
					code = DefaultExitCode
				}
				logger.Info().Msg("exit on http request")
				os.Exit(int(code))
			})
			mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
				rc := http.NewResponseController(w)
				dur := DefaultSlowDuration
				if d, err := time.ParseDuration(r.URL.Query().Get("duration")); err == nil {
					dur = d
				}
				interval := DefaultSlowInterval
				if d, err := time.ParseDuration(r.URL.Query().Get("interval")); err == nil {
					interval = d
				}

				logger.Info().Str("duration", dur.String()).Str("interval", interval.String()).Msg("slow responder started")
				ctx, cancel := context.WithTimeout(r.Context(), dur)
				defer cancel()
				w.Header().Add("Content-Type", "text/plain; charset=utf-8")
				w.Header().Set("X-Content-Type-Options", "nosniff")
				w.WriteHeader(http.StatusOK)

				fmt.Fprintf(w, "Will write for %s or until connection is aborted\n\n",
					dur.String())
				if err := rc.Flush(); err != nil {
					logger.Err(err).Msg("slow responder flush error")
					if errors.Is(err, http.ErrNotSupported) {
						return
					}
				}
				tick := time.Tick(interval)
				for {
					select {
					case <-tick:
						w.Write([]byte(GetSmiley()))
						if err := rc.Flush(); err != nil {
							logger.Err(err).Msg("slow responder flush error")
							if errors.Is(err, http.ErrNotSupported) {
								return
							}
						}
					case <-ctx.Done():
						logger.Info().Err(ctx.Err()).Msg("slow responder exiting")
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
			})
			mux.HandleFunc("/nothing", func(w http.ResponseWriter, r *http.Request) {
				dur := DefaultNothingDuration
				if d, err := time.ParseDuration(r.URL.Query().Get("duration")); err == nil {
					dur = d
				}
				logger.Info().Str("duration", dur.String()).Msg("nothing responder started")
				ctx, cancel := context.WithTimeout(r.Context(), dur)
				defer cancel()

				for {
					select {
					case <-ctx.Done():
						logger.Info().Err(ctx.Err()).Msg("nothing responder exiting")
						rc := http.NewResponseController(w)
						conn, _, err := rc.Hijack()
						if err == nil {
							defer conn.Close()
						}
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
			})

			server := &http.Server{
				Addr:    flags.WebListen,
				Handler: loggingMiddleware(mux),
			}

			mux.HandleFunc("/killhttp", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				fmt.Fprintln(w, "shutting down http server")
				logger.Info().Msg("killhttp requested")
				go func() {
					time.Sleep(100 * time.Millisecond)
					if err := server.Shutdown(context.Background()); err != nil {
						logger.Err(err).Msg("http server shutdown error")
					}
				}()
			})

			time.Sleep(effectiveSettings.WebDelay)
			logger.Info().Msg("listen")
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Fatal().Err(err).Msg("http listen error")
			}
			logger.Info().Msg("http server stopped")
		}()
	}

	if flags.CPUloadEnable {
		nWorkers := max(1, min(runtime.GOMAXPROCS(0), flags.CPULoadWorkers))
		logger.Info().Int("workers", nWorkers).Msg("starting cpu load")
		for range nWorkers {
			go startCPULoad()
		}
	}

	if flags.MemloadEnable {
		go func() {
			time.Sleep(flags.MemloadWait)
			startMemLoad()
		}()
	}
	for {
		time.Sleep(time.Second)
	}
}

type LoadStep struct {
	CPUPercent int
	MemMB      int
	Duration   time.Duration
}

func runLoadSteps(l zerolog.Logger, steps []LoadStep) {
	var data []byte
	t0 := time.Now()
	for i, step := range steps {
		logger := l.With().
			Int("step.#", i).
			Dur("elapsed", time.Now().Sub(t0).Round(100*time.Millisecond)).
			Logger()

		logger.Info().
			Int("cpu", step.CPUPercent).
			Int("mem", step.MemMB).
			Dur("dur", step.Duration).
			Msg("step starts")

		if step.MemMB > 0 {
			newData := make([]byte, step.MemMB*1024*1024)
			for j := 0; j < len(newData); j += 4096 {
				newData[j] = 1
			}
			data = newData
		} else {
			if data != nil {
				data = nil
				runtime.GC()
			}
		}
		if step.CPUPercent > 0 {
			doBusyWork(step.Duration, step.CPUPercent)
		} else {
			time.Sleep(step.Duration)
		}
	}
}

func doStartCPULoad() {
	testID := xid.New()
	logger := logger.With().Str("cpuload.id", testID.String()).Logger()
	logger.Info().Msg("load test starts")
	defer logger.Info().Msg("load test ended")

	const normal = 6 * time.Minute
	const burst = 30 * time.Second
	const sleep = 6 * time.Minute
	const shortSleep = 30 * time.Second
	const longSleep = 10 * time.Minute

	tests := []LoadStep{
		{Duration: burst, CPUPercent: 90},
		{Duration: shortSleep},
		{Duration: burst, CPUPercent: 90},
		{Duration: shortSleep},
		{Duration: normal, CPUPercent: 10},
		{Duration: sleep},
		{Duration: normal, CPUPercent: 20},
		{Duration: sleep},
		{Duration: normal, CPUPercent: 30},
		{Duration: sleep},
		{Duration: normal, CPUPercent: 40},
		{Duration: sleep},
		{Duration: normal, CPUPercent: 50},
		{Duration: sleep},
		{Duration: normal, CPUPercent: 60},
		{Duration: sleep},
		{Duration: normal, CPUPercent: 70},
		{Duration: sleep},
		{Duration: burst, CPUPercent: 90},
		{Duration: shortSleep},
		{Duration: burst, CPUPercent: 90},
		{Duration: sleep},
		{Duration: normal, CPUPercent: 70},
		{Duration: normal, CPUPercent: 50},
		{Duration: normal, CPUPercent: 20},
		{Duration: shortSleep},
		{Duration: burst, CPUPercent: 90},
		{Duration: longSleep},
		{Duration: burst, CPUPercent: 90},
		{Duration: longSleep},
		{Duration: burst, CPUPercent: 90},
		{Duration: sleep},
		{Duration: burst, CPUPercent: 50},
		{Duration: sleep},
		{Duration: burst, CPUPercent: 80},
		{Duration: sleep},
		{Duration: burst, CPUPercent: 70},
		{Duration: longSleep},
	}

	runLoadSteps(logger, tests)
}

func doStartMemLoad() {
	testID := xid.New()
	logger := logger.With().Str("memload.id", testID.String()).Logger()
	logger.Info().Msg("memload test starts")
	defer logger.Info().Msg("memload test ended")

	const short = 1 * time.Minute
	const long = 5 * time.Minute

	tests := []LoadStep{
		{Duration: short, MemMB: 100},
		{Duration: short, MemMB: 0},
		{Duration: short, MemMB: 300},
		{Duration: short, MemMB: 0},
		{Duration: long, MemMB: 300},
		{Duration: short, MemMB: 0},
		{Duration: long, MemMB: 400},
		{Duration: short, MemMB: 0},
	}

	runLoadSteps(logger, tests)
}

func doStartCombinedLoad() {
	testID := xid.New()
	logger := logger.With().Str("combinedload.id", testID.String()).Logger()
	logger.Info().Msg("combined load test starts")
	defer logger.Info().Msg("combined load test ended")

	tests := []LoadStep{
		{Duration: 1 * time.Minute, CPUPercent: 50, MemMB: 256},
		{Duration: 1 * time.Minute, CPUPercent: 10, MemMB: 512},
		{Duration: 1 * time.Minute, CPUPercent: 90, MemMB: 128},
		{Duration: 1 * time.Minute, CPUPercent: 0, MemMB: 0},
	}

	runLoadSteps(logger, tests)
}

func doBusyWork(duration time.Duration, percentage int) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	percentage = max(0, min(100, percentage))
	unitCycle := 100 * time.Millisecond
	workTime := time.Duration(percentage) * unitCycle / 100
	sleepTime := unitCycle - workTime
	endTime := time.Now().Add(duration)
	for time.Now().Before(endTime) {
		startWork := time.Now()
		for time.Since(startWork) < workTime {
			// consume CPU
		}
		time.Sleep(sleepTime)
	}
}

var isRunningLoad atomic.Int32

func Guard(fn func()) func() {
	return func() {
		if !isRunningLoad.CompareAndSwap(0, 1) {
			logger.Warn().Msg("a load generator is already running, skipping")
			return
		}
		defer isRunningLoad.Store(0)
		fn()
	}
}

var (
	startCPULoad      = Guard(doStartCPULoad)
	startMemLoad      = Guard(doStartMemLoad)
	startCombinedLoad = Guard(doStartCombinedLoad)
)

func GetSmiley() string {
	const (
		start = 0x1F600
		end   = 0x1F637
		// end   = 0x1F64F
	)
	codePoint := rand.IntN(end-start+1) + start
	return string(rune(codePoint))
}
