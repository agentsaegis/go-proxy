.PHONY: lint test test-e2e build clean

lint:
	go vet ./...

test:
	go test -race -coverprofile=coverage.out ./...

test-e2e:
	go test -race -tags e2e -v -count=1 ./e2e/...

build:
	go build -o bin/agentsaegis ./cmd/agentsaegis

clean:
	rm -rf bin coverage.out
