# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

troublemaker is a single-binary HTTP server that deliberately misbehaves, used to test
how infrastructure (Kubernetes probes, load balancers, autoscalers, deployment pipelines)
reacts to a faulty service. It can exit on demand, ignore shutdown signals, serve arbitrary
status codes, respond slowly or never, and generate sustained CPU/memory load. A companion
client drives realistic fault patterns against a running instance.

## Commands

```bash
go build -o troublemaker .              # build the server
go build ./cmd/troublemaker-client      # build the client driver
go test -race ./...                     # full test suite (matches CI)
go test -race -run TestName ./...       # single test
go vet ./...                            # vet

./troublemaker -web.listen 0.0.0.0:8092 -load.enable -load.type sine   # run server
./cmd/troublemaker-client/troublemaker-client -url http://localhost:8092 status -pattern flapping
```

The server is invoked with flags and serves docs at `/docs`. CI (`.github/workflows/docker.yml`)
runs `go test -race ./...`, then builds and pushes a Docker image to `ghcr.io` only on
`v*.*.*` tags.

## Architecture

Two `package main` programs in one module:

- **Server** (root: `main.go`, `load.go`, `logging.go`, `envinfo.go`, `bucket.go`) — the misbehaving service.
- **Client** (`cmd/troublemaker-client/main.go`) — a separate binary that hits a running
  server's `/status` and `/slow` endpoints following predefined or chaos patterns to simulate
  degradation over time. It is standalone and shares no code with the server.

### Configuration (`Flags` in main.go)

All settings live in the `Flags` struct, registered in `Flags.Register`. Parsing uses
`peterbourgon/ff/v3`, so **every flag is also settable via an UPPER_SNAKE_CASE env var**
(e.g. `-web.listen` → `WEB_LISTEN`) and via a config file (`-config`, PlainParser format).
This env-var mapping is what the Helm chart's `config:` block relies on. When adding a flag,
add it to `Flags`, register it, and (if user-facing) document it in `docs.html` and
`kubernetes/values.yaml` / `kubernetes/README.md`.

`Flags.EffectiveSettings()` resolves jitter and exit-probability against a seeded PRNG
(`rand.seed`) into a concrete `EffectiveSettings` — this is where randomized-but-reproducible
behavior is decided once at startup.

### HTTP handlers (main.go `main()`)

A single `http.ServeMux` wires all endpoints. Key design points:

- `rootHandler` makes `/` configurable at runtime: `-web.root` (or POSTing to
  `/set-root-handler`) sets a *view* that internally rewrites `/` to another endpoint
  (e.g. `status/200,404`), so the root can be made to misbehave without restart.
- `disableCachingMiddleware` + `loggingMiddleware` wrap the mux. `/debug/*` (pprof) bypasses
  the wrappers and goes straight to the mux.
- Fault endpoints: `/status/<codes>` (random pick from comma list), `/slow`, `/nothing`
  (hijacks and holds the connection), `/exit/?code=`, `/killhttp`, `/cache/<preset>`,
  `/set-headers/<k>/<v>/...`.
- The process never returns from a `for { time.Sleep }` loop in `main`; all work runs in
  goroutines. `os.Exit` is the normal termination path (exit-after, `/exit/`, subcommands).

Positional subcommands (`sleep <dur>`, `beat`, `aldryn-celery`) are handled after flag parsing
and short-circuit normal server startup. `aldryn-celery` is an alias entrypoint (see
`example/Dockerfile`) that runs a permanent CPU spike loop.

### Load generator (`load.go`)

`LoadGenerator` runs **one schedule at a time**, enforced by an atomic CAS in `guard()`.
A schedule is a `[]LoadStep` (cpu%, memMB, duration); `runLoadSteps` executes them sequentially,
allocating/touching memory and calling `doBusyWork` (a duty-cycle busy loop with `LockOSThread`)
for CPU. `CPUMax`/`MemMax` cap all generated loads. Schedule shapes (cpu, mem, combined, sine,
spike, random) are built by the `doStart*`/`Generate*` methods; `/load/random` and
`/load/seed/<n>` make a run reproducible from a seed. Aborting cancels the context via `Abort()`.

### Logging (`logging.go`)

zerolog writes to both stderr and an in-memory ring buffer (`LogBuffer`, size = `-log.size`),
exposed at `/logs`. Every request carries a process-wide `instance` id (`xid`) and `t`
(uptime). `/logs` and `/favicon.ico` are excluded from request logging to avoid feedback loops.

### envinfo.go

Reads cgroup v2 (falling back to v1) memory/CPU limits from `/proc` and `/sys/fs/cgroup`,
logged once at startup so container resource limits are visible in logs.

### S3 bucket (`bucket.go`)

When any `BUCKET_*` flag/env var is set (`bucketConfigured()`), `checkBucket` runs once at
startup: it builds an aws-sdk-go-v2 S3 client (static creds from `BUCKET_ACCESS_KEY_ID` /
`BUCKET_SECRET_ACCESS_KEY`, optional `BUCKET_ENDPOINT` with path-style addressing, falling
back to the default AWS credential chain) and verifies connectivity by listing the bucket
root via `ListObjectsV2` (delimiter `/`); `BUCKET_NAME` is required. With
`BUCKET_CRASH_ON_ERROR=true` a failed check
calls `logger.Fatal` to crash the process on startup. `BucketSecretAccessKey` is a
`SecretString` whose `MarshalJSON` redacts it so it never leaks into `/info` or `/docs`
(note: the startup "env" log still dumps the raw process environment).

## Deployment

`Dockerfile` builds a static binary into `gcr.io/distroless/static-debian12`, exposing 8092.
A Helm chart lives in `kubernetes/`; it exposes every flag through the env-var mapping under
`config:` in `values.yaml`.
