# syntax=docker/dockerfile:1
from golang:1.26.3 as build

workdir /app
copy go.mod go.sum /app/
run --mount=type=cache,target=/go/pkg/mod \
    go mod download
copy . /app
run --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -o /app/troublemaker

from gcr.io/distroless/static-debian12
expose 8092
copy --from=build /app/troublemaker /
entrypoint ["/troublemaker"]
