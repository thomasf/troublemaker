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
	"math/rand/v2"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"net/http/pprof"

	"github.com/peterbourgon/ff/v3"
	"github.com/rs/xid"
	"github.com/rs/zerolog/log"
)

const (
	DefaultExitCode        = 1
	DefaultSlowDuration    = 5 * time.Minute
	DefaultSlowInterval    = 100 * time.Millisecond
	DefaultNothingDuration = 5 * time.Minute
)

var (
	startTime = time.Now().UTC()
	t0        = time.Now()
)

var instanceID = xid.New().String()

func disableCachingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, proxy-revalidate")
		next.ServeHTTP(w, r)
	})
}

func newSetHeadersHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fullPath := r.URL.Path
		ext := path.Ext(fullPath)
		contentType := mime.TypeByExtension(ext)
		if contentType == "" {
			contentType = "text/plain"
		}
		w.Header().Set("Content-Type", contentType)

		pathParts := strings.TrimPrefix(fullPath, "/set-headers/")
		pathParts = strings.TrimSuffix(pathParts, ext)

		parts := strings.Split(pathParts, "/")
		for i := 0; i < len(parts)-1; i += 2 {
			key := parts[i]
			val := parts[i+1]
			if key != "" && val != "" {
				w.Header().Set(key, val)
			}
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "Set Headers page\n")
		fmt.Fprintf(w, "Path: %s\n", r.URL.Path)
		fmt.Fprintf(w, "Time: %s\n", time.Now().Format(time.RFC3339Nano))
		fmt.Fprintln(w, "\nResponse Headers:")
		for k, v := range w.Header() {
			fmt.Fprintf(w, "%s: %s\n", k, strings.Join(v, ", "))
		}
	})
}

func newCacheHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fullPath := r.URL.Path
		ext := path.Ext(fullPath)
		contentType := mime.TypeByExtension(ext)
		if contentType == "" {
			contentType = "text/plain"
		}
		w.Header().Set("Content-Type", contentType)

		preset := strings.TrimPrefix(fullPath, "/cache/")
		preset = strings.TrimSuffix(preset, ext)

		switch preset {
		case "no-cache":
			w.Header().Set("Cache-Control", "no-cache")
		case "no-store":
			w.Header().Set("Cache-Control", "no-store")
		case "public-1h":
			w.Header().Set("Cache-Control", "public, max-age=3600")
		case "private-1h":
			w.Header().Set("Cache-Control", "private, max-age=3600")
		default:
			if preset != "" {
				http.Error(w, "unknown preset", http.StatusNotFound)
				return
			}
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "Cache test page\n")
		fmt.Fprintf(w, "Path: %s\n", r.URL.Path)
		fmt.Fprintf(w, "Time: %s\n", time.Now().Format(time.RFC3339Nano))
		fmt.Fprintln(w, "\nResponse Headers:")
		for k, v := range w.Header() {
			fmt.Fprintf(w, "%s: %s\n", k, strings.Join(v, ", "))
		}
	})
}

func newStatusHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pathParts := strings.TrimPrefix(r.URL.Path, "/status/")
		codes := strings.Split(pathParts, ",")
		var validCodes []int
		for _, c := range codes {
			if code, err := strconv.Atoi(strings.TrimSpace(c)); err == nil && code >= 100 && code <= 599 {
				validCodes = append(validCodes, code)
			}
		}

		if len(validCodes) == 0 {
			validCodes = []int{http.StatusOK}
		}

		selectedCode := validCodes[rand.IntN(len(validCodes))]

		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(selectedCode)

		fmt.Fprintf(w, "<html><body><h1>Status: %d</h1><p>Randomly selected from: %v</p><p>Time: %s</p></body></html>",
			selectedCode, validCodes, time.Now().Format(time.RFC3339Nano))
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
		fmt.Fprintf(w, "uptime %s\n", time.Since(t0).String())

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
		fmt.Fprintf(w, "uptime %s\n", time.Since(t0).String())
	}
}

type rootHandler struct {
	mu             sync.RWMutex
	view           string
	defaultHandler http.Handler
	mux            *http.ServeMux
}

func (h *rootHandler) SetView(view string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.view = strings.TrimPrefix(view, "/")
}

func (h *rootHandler) GetView() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.view
}

func (h *rootHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	view := h.GetView()
	if view == "" {
		h.defaultHandler.ServeHTTP(w, r)
		return
	}

	path, query, _ := strings.Cut(view, "?")
	r.URL.Path = "/" + path
	if query != "" {
		if r.URL.RawQuery != "" {
			r.URL.RawQuery = query + "&" + r.URL.RawQuery
		} else {
			r.URL.RawQuery = query
		}
	}
	h.mux.ServeHTTP(w, r)
}

func newSetRootHandler(rh *rootHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		view := r.FormValue("view")
		rh.SetView(view)
		http.Redirect(w, r, "/docs", http.StatusFound)
	})
}

//go:embed docs.html
var docsData []byte

func newDocsHandler(flags Flags, effective EffectiveSettings, usage string, rh *rootHandler) func(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.New("docs").Parse(string(docsData))
	if err != nil {
		panic(err)
	}

	return func(w http.ResponseWriter, r *http.Request) {
		flagsData, _ := json.MarshalIndent(flags, "", "  ")
		effectiveData, _ := json.MarshalIndent(effective, "", "  ")

		buildInfoStr := ""
		buildInfo, ok := debug.ReadBuildInfo()
		if ok {
			buildInfoStr = buildInfo.String()
		}

		data := struct {
			Flags                   string
			EffectiveSettings       string
			Usage                   string
			BuildInfo               string
			DefaultExitCode         int
			DefaultSlowDuration     time.Duration
			DefaultSlowInterval     time.Duration
			DefaultNothingDuration  time.Duration
			LoadMemMax              int
			LoadCPUMax              int
			RandomLoadTotalDuration time.Duration
			CurrentRootView         string
		}{
			Flags:                   string(flagsData),
			EffectiveSettings:       string(effectiveData),
			Usage:                   usage,
			BuildInfo:               buildInfoStr,
			DefaultExitCode:         DefaultExitCode,
			DefaultSlowDuration:     DefaultSlowDuration,
			DefaultSlowInterval:     DefaultSlowInterval,
			DefaultNothingDuration:  DefaultNothingDuration,
			LoadMemMax:              flags.LoadMemMax,
			LoadCPUMax:              flags.LoadCPUMax,
			RandomLoadTotalDuration: RandomLoadTotalDuration,
			CurrentRootView:         rh.GetView(),
		}

		w.Header().Add("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		if err := tmpl.Execute(w, data); err != nil {
			logger.Err(err).Msg("template execution error")
		}
	}
}

// Flags .
type Flags struct {
	WebEnable       bool          `json:"web.enable"`
	WebListen       string        `json:"web.listen"`
	WebDelay        time.Duration `json:"web.delay"`
	WebDelayJitter  time.Duration `json:"web.delay.jitter"`
	WebRoot         string        `json:"web.root"`
	ExitAfter       time.Duration `json:"exit.after"`
	ExitAfterJitter time.Duration `json:"exit.after.jitter"`
	ExitPercent     int           `json:"exit.percent"`
	ExitCode        int           `json:"exit.code"`
	IgnoreSignals   bool          `json:"ignore.signals"`
	LoadEnable      bool          `json:"load.enable"`
	LoadType        string        `json:"load.type"`
	LoadCPUMax      int           `json:"load.cpu.max"`
	LoadMemMax      int           `json:"load.mem.max"`

	LoadWait time.Duration `json:"load.wait"`
	LogSize  int           `json:"log.size"`

	PprofEnable bool `json:"pprof.enable"`

	RandSeed uint64 `json:"rand.seed1"`
}

func (f *Flags) Register(fs *flag.FlagSet) {
	fs.BoolVar(&f.WebEnable, "web.enable", true, "enable http server")
	fs.StringVar(&f.WebListen, "web.listen", "0.0.0.0:8092", "http server bind addr")
	fs.DurationVar(&f.WebDelay, "web.delay", 0, "sleep duration before starting http server")
	fs.DurationVar(&f.WebDelayJitter, "web.delay.jitter", 0, "delay +/- jitter")
	fs.StringVar(&f.WebRoot, "web.root", "", "handler to use for / (e.g. status/200,404)")

	fs.DurationVar(&f.ExitAfter, "exit.after", 0, "exit with exit code 1 if duration > 0, 1ns=exit asap")
	fs.DurationVar(&f.ExitAfterJitter, "exit.after.jitter", 0, "exit after +/- jitter")
	fs.IntVar(&f.ExitPercent, "exit.percent", 100, "% chance to exit if exit.after is set")

	fs.IntVar(&f.ExitCode, "exit.code", DefaultExitCode, "exit code when exiting")

	fs.BoolVar(&f.IgnoreSignals, "signals.ignore", false, "ignore shutdown signals")

	fs.BoolVar(&f.LoadEnable, "load.enable", false, "enable load generator at startup")
	fs.StringVar(&f.LoadType, "load.type", "random", "type of load to generate (cpu, mem, combined, sine, spike, random)")
	fs.IntVar(&f.LoadCPUMax, "load.cpu.max", 85, "maximum cpu load in percent")
	fs.IntVar(&f.LoadMemMax, "load.mem.max", 666, "maximum memory load in MB")
	fs.DurationVar(&f.LoadWait, "load.wait", 0, "wait duration before starting load")

	fs.IntVar(&f.LogSize, "log.size", 10000, "number of log lines to keep in memory")

	fs.BoolVar(&f.PprofEnable, "pprof.enable", false, "enable pprof at /debug/pprof/")

	fs.Uint64Var(&f.RandSeed, "rand.seed", rand.Uint64(), "seed for random generator")
}

func (f Flags) EffectiveSettings() EffectiveSettings {
	r := rand.New(rand.NewPCG(f.RandSeed, f.RandSeed))

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
	logger.Info().
		Time("t0", startTime).
		Strs("environment", os.Environ()).
		Msg("started")

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
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
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
					Dur("after", time.Since(startTime)).
					Msg("ignore signal")
			}
		}()
	}

	effectiveSettings := flags.EffectiveSettings()

	logger.Info().Interface("data", &flags).Msg("flags")
	logger.Info().Interface("data", &effectiveSettings).Msg("effective settings")

	lg := NewLoadGenerator(logger)
	lg.CPUMax = flags.LoadCPUMax
	lg.MemMax = flags.LoadMemMax

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
		case "aldryn-celery":
			log.Info().Msg("start as aldryn celery")
			log.Info().Msg("enter constant cpu spike mode")
			for {
				lg.StartSpikeLoad()
			}

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
			rh := &rootHandler{
				mux:            mux,
				defaultHandler: http.HandlerFunc(newRootHandler()),
			}
			rh.SetView(flags.WebRoot)

			mux.Handle("/", rh)
			mux.HandleFunc("/docs", newDocsHandler(flags, effectiveSettings, usage, rh))
			mux.Handle("/set-root-handler", newSetRootHandler(rh))
			mux.HandleFunc("/info", newInfoHandler(flags, effectiveSettings))
			mux.HandleFunc("/logs", newLogsHandler())
			mux.Handle("/status/", newStatusHandler())
			mux.Handle("/cache/", newCacheHandler())
			mux.Handle("/set-headers/", newSetHeadersHandler())

			if flags.PprofEnable {
				mux.HandleFunc("/debug/pprof/", pprof.Index)
				mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
				mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
				mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
				mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
			}

			mux.HandleFunc("/load/status", lg.StatusHandler)
			mux.HandleFunc("/load/abort", lg.AbortHandler)
			mux.HandleFunc("/load/cpu", lg.CPULoadHandler)
			mux.HandleFunc("/load/mem", lg.MemLoadHandler)
			mux.HandleFunc("/load/combined", lg.CombinedLoadHandler)
			mux.HandleFunc("/load/sine", lg.SineLoadHandler)
			mux.HandleFunc("/load/spike", lg.SpikeLoadHandler)
			mux.HandleFunc("/load/random", lg.RandomLoadHandler)
			mux.HandleFunc("/load/seed/", lg.SeedLoadHandler)
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

				<-ctx.Done()
				logger.Info().Err(ctx.Err()).Msg("nothing responder exiting")
				rc := http.NewResponseController(w)
				conn, _, err := rc.Hijack()
				if err == nil {
					defer conn.Close()
				}
			})

			wrappedMux := loggingMiddleware(disableCachingMiddleware(mux))
			server := &http.Server{
				Addr: flags.WebListen,
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasPrefix(r.URL.Path, "/debug/") {
						mux.ServeHTTP(w, r)
					} else {
						wrappedMux.ServeHTTP(w, r)
					}
				}),
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

	if flags.LoadEnable {
		go func() {
			if flags.LoadWait > 0 {
				time.Sleep(flags.LoadWait)
			}
			switch strings.ToLower(flags.LoadType) {
			case "cpu":
				logger.Info().Msg("starting cpu load")
				lg.StartCPULoad()
			case "mem":
				logger.Info().Msg("starting mem load")
				lg.StartMemLoad()
			case "mem+cpu", "combined":
				logger.Info().Msg("starting combined load")
				lg.StartCombinedLoad()
			case "sine":
				logger.Info().Msg("starting sine load")
				lg.StartSineLoad()
			case "spike":
				logger.Info().Msg("starting spike load")
				lg.StartSpikeLoad()
			case "random":
				logger.Info().Msg("starting random load")
				lg.StartSchedule(flags.RandSeed, 0, "cpu,mem")
			default:
				logger.Warn().Str("type", flags.LoadType).Msg("unknown load type")
			}
		}()
	}

	for {
		time.Sleep(time.Second)
	}
}

func GetSmiley() string {
	const (
		start = 0x1F600
		end   = 0x1F637
		// end   = 0x1F64F
	)
	codePoint := rand.IntN(end-start+1) + start
	return string(rune(codePoint))
}
