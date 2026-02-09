from golang:1.25.7 as build
arg VERSION=latest
run CGO_ENABLED=0 GOOS=linux go install github.com/thomasf/troublemaker@${VERSION}
from gcr.io/distroless/static-debian12
expose 8092
copy --from=build /go/bin/troublemaker /
entrypoint ["/troublemaker"]
