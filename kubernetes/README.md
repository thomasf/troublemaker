# troublemaker Helm chart

Helm chart for [troublemaker](https://github.com/thomasf/troublemaker), a
configurable fault/load injection HTTP server.

The chart deploys a `Deployment` + `Service` (and optionally an `Ingress`,
`ServiceAccount`, and `PriorityClass`). By default it pulls the public image
`ghcr.io/thomasf/troublemaker:0.1` straight from GitHub Container Registry —
no local build or image import required.

## Quick start

```bash
helm install troublemaker kubernetes/ -n troublemaker --create-namespace
```

Wait for it to be ready:

```bash
helm install troublemaker kubernetes/ -n troublemaker --create-namespace --wait
```

Reach the HTTP server (ClusterIP by default):

```bash
kubectl -n troublemaker port-forward svc/troublemaker 8092:8092
# then open http://127.0.0.1:8092/docs
```

## Configuration

All of troublemaker's settings are exposed under `config:`. Each key is passed
to the container as an environment variable; keys left as `null` fall back to
the binary's built-in default. Durations use Go syntax (`5m`, `100ms`, `1ns`).
Booleans and ints must be quoted as strings.

| Key | Default | Description |
| --- | --- | --- |
| `WEB_ENABLE` | `true` | enable http server |
| `WEB_LISTEN` | `0.0.0.0:8092` | http server bind addr |
| `WEB_DELAY` | `0s` | sleep before starting http server |
| `WEB_DELAY_JITTER` | `0s` | delay +/- jitter |
| `WEB_ROOT` | "" | handler to use for `/` (e.g. `status/200,404`) |
| `EXIT_AFTER` | `0s` | exit after duration if >0, `1ns` = exit asap |
| `EXIT_AFTER_JITTER` | `0s` | exit.after +/- jitter |
| `EXIT_PERCENT` | `100` | % chance to exit if `EXIT_AFTER` is set |
| `EXIT_CODE` | `1` | exit code when exiting |
| `SIGNALS_IGNORE` | `false` | ignore shutdown signals |
| `LOAD_ENABLE` | `false` | enable load generator at startup |
| `LOAD_TYPE` | `random` | `cpu`, `mem`, `combined`, `sine`, `spike`, `static`, `random` |
| `LOAD_CPU_MAX` | `85` | maximum cpu load in percent |
| `LOAD_MEM_MAX` | `666` | maximum memory load in MB |
| `LOAD_WAIT` | `0s` | wait before starting load |
| `LOG_SIZE` | `10000` | number of log lines to keep in memory |
| `PPROF_ENABLE` | `false` | enable pprof at `/debug/pprof/` |
| `RAND_SEED` | random | seed for random generator |
| `BUCKET_REGION` | "" | s3 bucket region |
| `BUCKET_ENDPOINT` | "" | endpoint url for s3-compatible storage |
| `BUCKET_ACCESS_KEY_ID` | "" | static s3 access key id |
| `BUCKET_SECRET_ACCESS_KEY` | "" | static s3 secret access key |
| `BUCKET_NAME` | "" | bucket whose root is listed (required if any `BUCKET_*` is set) |
| `BUCKET_TIMEOUT` | `10s` | startup connectivity check timeout |
| `BUCKET_CRASH_ON_ERROR` | `false` | crash on startup if the bucket cannot be reached |
| `POSTGRES_DSN` | "" | postgresql connection string (url or keyword/value form) |
| `POSTGRES_HOST` | "" | postgresql host, overrides the dsn |
| `POSTGRES_PORT` | "" | postgresql port, overrides the dsn |
| `POSTGRES_USER` | "" | postgresql user, overrides the dsn |
| `POSTGRES_PASSWORD` | "" | postgresql password, overrides the dsn |
| `POSTGRES_DBNAME` | "" | postgresql database name, overrides the dsn |
| `POSTGRES_SSLMODE` | "" | postgresql sslmode (disable, require, verify-ca, verify-full) |
| `POSTGRES_TIMEOUT` | `10s` | startup connectivity check timeout |
| `POSTGRES_CRASH_ON_ERROR` | `false` | crash on startup if the database cannot be reached |

See `values.yaml` for the full list of chart values (image, resources, probes,
ingress, scheduling, security context, etc.).

### Install with config + priority

Pod scheduling priority is set via `priorityClass` (create one) and/or
`priorityClassName` (reference an existing one).

```bash
helm install troublemaker kubernetes/ -n troublemaker --create-namespace \
  --set config.LOAD_ENABLE=true \
  --set config.LOAD_TYPE=cpu \
  --set config.WEB_ROOT="status/200" \
  --set priorityClass.create=true \
  --set priorityClass.value=1000
```

### Install with a values file

```yaml
# my-values.yaml
config:
  LOAD_ENABLE: "true"
  LOAD_TYPE: cpu
  WEB_ROOT: "status/200"
priorityClass:
  create: true
  value: 1000
```

```bash
helm install troublemaker kubernetes/ -n troublemaker --create-namespace -f my-values.yaml
```

To reference an existing PriorityClass instead of creating one:

```bash
helm install troublemaker kubernetes/ -n troublemaker --create-namespace \
  --set priorityClassName=system-cluster-critical
```

## Image

The chart defaults to the floating `0.1` minor-version tag with
`pullPolicy: Always`, so every pod start re-pulls the newest `0.1.x` patch.

```bash
# pick up a newly published 0.1.x patch
kubectl -n troublemaker rollout restart deployment/troublemaker

# pin an exact patch instead of the floating 0.1 tag
helm install troublemaker kubernetes/ -n troublemaker --create-namespace \
  --set image.tag=0.1.5
```

## Common commands

```bash
# preview rendered manifests without installing
helm template troublemaker kubernetes/

# lint the chart
helm lint kubernetes/

# install / upgrade
helm install troublemaker kubernetes/ -n troublemaker --create-namespace -f my-values.yaml
helm upgrade troublemaker kubernetes/ -n troublemaker -f my-values.yaml

# status and resources
helm -n troublemaker status troublemaker
kubectl -n troublemaker get pods
kubectl -n troublemaker logs deploy/troublemaker -f

# reach the server
kubectl -n troublemaker port-forward svc/troublemaker 8092:8092

# uninstall
helm uninstall troublemaker -n troublemaker
```

## Local cluster (k3d) notes

The image is public, so k3d/kind pull it directly — nothing extra needed. If
you ever want to test a locally built image instead:

```bash
docker build -t ghcr.io/thomasf/troublemaker:dev .
k3d image import ghcr.io/thomasf/troublemaker:dev -c mycluster
helm install troublemaker kubernetes/ -n troublemaker --create-namespace \
  --set image.tag=dev --set image.pullPolicy=IfNotPresent
```
