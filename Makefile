.PHONY: lint test build clean

lint:
	go vet ./...

test:
	go test -race -coverprofile=coverage.out ./...

build:
	go build -o bin/agentsaegis ./cmd/agentsaegis

clean:
	rm -rf bin coverage.out
