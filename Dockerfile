from golang:1.26 as build

workdir /app
copy go.mod go.sum /app/
run go mod download
copy . /app
run CGO_ENABLED=0 GOOS=linux go build -o /app/troublemaker

from gcr.io/distroless/static-debian12
expose 8092
copy --from=build /app/troublemaker /
entrypoint ["/troublemaker"]
