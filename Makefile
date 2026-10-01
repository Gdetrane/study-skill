.PHONY: help test lint coverage build clean

help:
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "%-10s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

test: ## Run all tests with the race detector
	go test -race ./...

lint: ## Check formatting and run go vet
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...

coverage: ## Run tests with a coverage profile
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -1

build: ## Build the study binary
	go build -o study ./cmd/study/

clean: ## Remove generated local artifacts
	rm -f coverage.out study
