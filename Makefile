# agentbus build and verification targets. `make verify` is the done gate.
.PHONY: build build-all fmt-check vet lint test test-race verify

build: ## Build the agentbus binary the way release/release.sh does (no cgo)
	CGO_ENABLED=0 go build -trimpath -o agentbus .

build-all: ## Compile every package, including cmd and test helpers
	CGO_ENABLED=0 go build ./...

fmt-check: ## Fail if any tracked Go file is not gofmt-clean
	@out=$$(gofmt -l $$(git ls-files '*.go')); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet: ## go vet (also compiles every _test.go)
	go vet ./...

lint: ## golangci-lint v2 with its default linters (no .golangci.yml in this repo)
	golangci-lint run ./...

test: ## Unit tests (none need live services)
	go test ./...

test-race: ## Unit tests under the race detector (slower; the mcpserver tests rebuild the child binary with -race)
	go test -race ./...

verify: ## Done gate: build, vet, gofmt, lint, tests, in that order
	$(MAKE) build
	$(MAKE) build-all
	$(MAKE) vet
	$(MAKE) fmt-check
	$(MAKE) lint
	$(MAKE) test
	@echo "verify: OK"
