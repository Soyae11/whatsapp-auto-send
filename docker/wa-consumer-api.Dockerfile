# wa-consumer-api (../wa-consumer-api). Built with `context: ./wa-consumer-api`, so paths
# below are relative to that directory and ./wa-consumer-api/.dockerignore still applies.
# wa-consumer-api's go.mod replaces the `wa-shared` module with a local sibling directory
# (see ../go.work), so the build also needs that source — pulled in here as the named
# build context `shared` (see docker-compose.yml's `additional_contexts`), not from the
# module proxy.

ARG GO_VERSION=1.25

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY --from=shared . ./wa-shared

WORKDIR /src/wa-consumer-api
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X 'main.version=${VERSION}'" \
    -o /out/app ./cmd/api

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10001 wa-consumer-api
USER wa-consumer-api
COPY --from=build /out/app /usr/local/bin/app
ENTRYPOINT ["/usr/local/bin/app"]
