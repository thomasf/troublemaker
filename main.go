package main

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/peterbourgon/ff/v3"
	"github.com/rs/xid"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var logger zerolog.Logger

var startTime = time.Now().UTC()
var t0 = time.Now()

func init() {
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: time.RFC3339Nano,
	})

	logger = log.With().
		Str("instance", xid.New().String()).
		Logger().
		Hook(zerolog.HookFunc(func(e *zerolog.Event, level zerolog.Level, message string) {
			e.Str("t", time.Now().Sub(t0).String())
		}))
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	logger.Info().Msg("/ requested")
	fmt.Fprintf(w, "numcpu: %v\n", runtime.NumCPU())
	fmt.Fprintf(w, "maxprocs: %v\n", runtime.GOMAXPROCS(0))
}

// Flags .
type Flags struct {
	WebListen       string
	WebDelay        time.Duration
	WebDelayJitter  time.Duration
	WebEnable       bool
	ExitAfter       time.Duration
	ExitAfterJitter time.Duration
	ExitPercent     int
	ExitCode        int
	IgnoreSignals   bool
	CPUloadEnable   bool
	CPULoadWorkers  int
	CPULoadDuration time.Duration

	RandSeed1 uint64
	RandSeed2 uint64
}

func (f *Flags) Register(fs *flag.FlagSet) {
	fs.BoolVar(&f.WebEnable, "web.enable", true, "enable http server")
	fs.StringVar(&f.WebListen, "web.listen", "0.0.0.0:8092", "http server bind addr")
	fs.DurationVar(&f.WebDelay, "web.delay", 0, "sleep duration before starting http server")
	fs.DurationVar(&f.WebDelayJitter, "web.delay.jitter", 0, "delay +/- jitter")

	fs.DurationVar(&f.ExitAfter, "exit.after", 0, "exit with exit code 1 if duration > 0, 1ns=exit asap")
	fs.DurationVar(&f.ExitAfterJitter, "exit.after.jitter", 0, "exit after +/- jitter")
	fs.IntVar(&f.ExitPercent, "exit.percent", 100, "% chance to exit if exit.after is set")

	fs.IntVar(&f.ExitCode, "exit.code", 1, "exit code when exiting")

	fs.BoolVar(&f.IgnoreSignals, "signals.ignore", false, "ignore shutdown signals")

	fs.BoolVar(&f.CPUloadEnable, "cpuload.enable", false, "enable cpu load generator")
	fs.IntVar(&f.CPULoadWorkers, "cpuload.workers", 1, "number of concurrent goroutines, won't go over max")

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
	ExitAfter  time.Duration
	WebDelay   time.Duration
	ShouldExit bool
}

func main() {

	logger.Info().Time("t0", startTime).Msg("started")
	var flags Flags

	fs := flag.NewFlagSet("troublemaker", flag.ContinueOnError)
	flags.Register(fs)

	if err := ff.Parse(fs, os.Args[1:],
		ff.WithEnvVarNoPrefix(),
		ff.WithConfigFileFlag("config"),
		ff.WithConfigFileParser(ff.PlainParser),
	); err != nil {
		logger.Err(err).Msg("could not parse flags")
		os.Exit(1)
	}
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
			mux.HandleFunc("/", rootHandler)
			mux.HandleFunc("/exit/", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				codeStr := r.URL.Query().Get("code")
				code, err := strconv.ParseInt(codeStr, 10, 64)
				if err != nil || code < 0 || code > 127 {
					code = 1
				}
				logger.Info().Msg("exit on http request")
				os.Exit(int(code))
			})
			time.Sleep(effectiveSettings.WebDelay)
			logger.Info().Msg("listen")
			if err := http.ListenAndServe(flags.WebListen, mux); err != nil {
				logger.Fatal().Err(err).Msg("http listen error")
			}
		}()
	}

	if flags.CPUloadEnable {
		nWorkers := max(1, min(runtime.GOMAXPROCS(0), flags.CPULoadWorkers))
		logger.Info().Int("workers", nWorkers).Msg("starting cpu load")
		for range nWorkers {
			go startCPULoad()
		}
	}

	for {
		time.Sleep(time.Second)
	}
}

func startCPULoad() {
	testID := xid.New()
	logger := logger.With().Str("cpuload.id", testID.String()).Logger()
	logger.Info().Msg("load test starts")
	defer logger.Info().Msg("load test ended")

	const normal = 6 * time.Minute
	const burst = 30 * time.Second
	const sleep = 6 * time.Minute
	const shortSleep = 30 * time.Second
	const longSleep = 10 * time.Minute

	type Action struct {
		Percent  int
		Duration time.Duration
	}

	tests := []Action{
		{Duration: burst, Percent: 90},
		{Duration: shortSleep},
		{Duration: burst, Percent: 90},
		{Duration: shortSleep},
		{Duration: normal, Percent: 10},
		{Duration: sleep},
		{Duration: normal, Percent: 20},
		{Duration: sleep},
		{Duration: normal, Percent: 30},
		{Duration: sleep},
		{Duration: normal, Percent: 40},
		{Duration: sleep},
		{Duration: normal, Percent: 50},
		{Duration: sleep},
		{Duration: normal, Percent: 60},
		{Duration: sleep},
		{Duration: normal, Percent: 70},
		{Duration: sleep},
		{Duration: burst, Percent: 90},
		{Duration: shortSleep},
		{Duration: burst, Percent: 90},
		{Duration: sleep},
		{Duration: normal, Percent: 70},
		{Duration: normal, Percent: 50},
		{Duration: normal, Percent: 20},
		{Duration: shortSleep},
		{Duration: burst, Percent: 90},
		{Duration: longSleep},
		{Duration: burst, Percent: 90},
		{Duration: longSleep},
		{Duration: burst, Percent: 90},
		{Duration: sleep},
		{Duration: burst, Percent: 50},
		{Duration: sleep},
		{Duration: burst, Percent: 80},
		{Duration: sleep},
		{Duration: burst, Percent: 70},
		{Duration: longSleep},
	}

	// {
	// 	var sb strings.Builder
	// 	var cum time.Duration
	// 	for i, action := range tests {
	// 		sb.WriteRune('\n')
	// 		fmt.Fprintf(&sb, "%03d %s ", i, cum.String())
	// 		if action.Percent == 0 {
	// 			fmt.Fprintf(&sb, "sleep for %s", action.Duration)
	// 			continue
	// 		}
	// 		sb.WriteString(fmt.Sprintf("use %v%% cpu for %s", action.Percent, action.Duration.String()))
	// 		cum += action.Duration

	// 	}

	// 	log.Info().Msg("test plan:" + sb.String())
	// }
	t0 := time.Now()
	for i, action := range tests {
		logger := logger.With().Int("test.#", i).Dur("test.time", time.Now().Sub(t0).Round(100*time.Millisecond)).Logger()
		if action.Percent == 0 {
			logger.Info().Msg("Sleep for " + action.Duration.String())
			time.Sleep(action.Duration)
			continue
		}
		logger.Info().Msg(fmt.Sprintf("generate %v%% load for %s", action.Percent, action.Duration.String()))
		doBusyWork(action.Duration, action.Percent)
	}
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
