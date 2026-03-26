.PHONY: lint test test-e2e test-live build clean qa qa-live qa-docker qa-docker-claude qa-docker-copilot

lint:
	go vet ./...

test:
	go test -race -coverprofile=coverage.out ./...

test-e2e:
	go test -race -tags e2e -v -count=1 ./e2e/...

test-live: build
	go test -race -tags live -v -count=1 -timeout 10m ./e2e/...

build:
	go build -o bin/agentsaegis ./cmd/agentsaegis

clean:
	rm -rf bin coverage.out

qa: build
	bash scripts/qa.sh

qa-live: build
	bash scripts/qa-live.sh

qa-docker:
	bash scripts/qa-docker.sh --all

qa-docker-claude:
	bash scripts/qa-docker.sh --claude

qa-docker-copilot:
	bash scripts/qa-docker.sh --copilot
