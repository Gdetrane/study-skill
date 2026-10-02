.PHONY: help test lint coverage build release-check release-snapshot clean

help:
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "%-17s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

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

GORELEASER ?= goreleaser

release-check: ## Validate the GoReleaser configuration
	$(GORELEASER) check

release-snapshot: ## Build every release artefact into dist/ without publishing
	$(GORELEASER) release --snapshot --clean --skip=publish

clean: ## Remove generated local artifacts
	rm -rf coverage.out study dist completions manpages
