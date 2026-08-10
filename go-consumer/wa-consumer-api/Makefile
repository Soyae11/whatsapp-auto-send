.PHONY: build vet test up down logs

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X 'main.version=$(VERSION)'

build:
	go build -ldflags "$(LDFLAGS)" -o bin/api ./cmd/api

vet:
	go vet ./...

test:
	go test -race -count=1 ./...

# The stack is one compose file at the repo root — see ../../README.md. These targets run
# it from here for convenience; they are not a separate stack.
up:
	docker compose -f ../../docker-compose.yml up -d --build wa-consumer-api

down:
	docker compose -f ../../docker-compose.yml down

logs:
	docker compose -f ../../docker-compose.yml logs -f wa-consumer-api
