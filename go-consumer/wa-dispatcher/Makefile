.PHONY: build vet test up down logs

build:
	go build -o bin/worker ./cmd/worker

vet:
	go vet ./...

test:
	go test -race -count=1 ./...

# The stack is one compose file at the repo root — see ../../README.md. These targets run
# it from here for convenience; they are not a separate stack.
up:
	docker compose -f ../../docker-compose.yml up -d --build wa-dispatcher

down:
	docker compose -f ../../docker-compose.yml down

logs:
	docker compose -f ../../docker-compose.yml logs -f wa-dispatcher
