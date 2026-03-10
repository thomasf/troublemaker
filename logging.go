package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var logger zerolog.Logger

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
			e.Str("t", time.Since(t0).String())
		}))
}

func newLogsHandler() func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		logBuffer.WriteTo(w)
	}
}

type loggingMiddlewareResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (rw *loggingMiddlewareResponseWriter) WriteHeader(code int) {
	if rw.wroteHeader {
		return
	}
	rw.status = code
	rw.wroteHeader = true
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *loggingMiddlewareResponseWriter) Write(p []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.ResponseWriter.Write(p)
}

func (rw *loggingMiddlewareResponseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

var logIgnored = map[string]bool{
	"/logs":        true,
	"/favicon.ico": true,
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &loggingMiddlewareResponseWriter{ResponseWriter: w, status: http.StatusOK}
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
