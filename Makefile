# agentbus build and verification targets. `make verify` is the done gate.
.PHONY: build build-all fmt-check vet vet-cross lint test test-race verify release-check

build: ## Build the agentbus binary the way release/release.sh does (no cgo)
	CGO_ENABLED=0 go build -trimpath -o agentbus .

build-all: ## Compile every package, including cmd and test helpers
	CGO_ENABLED=0 go build ./...

fmt-check: ## Fail if any Go file is not gofmt-clean
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet: ## go vet (also compiles every _test.go)
	go vet ./...

lint: ## golangci-lint v2 with its default linters (no .golangci.yml in this repo)
	golangci-lint run ./...

test: ## Unit tests (none need live services)
	go test ./...

vet-cross: ## Compile every package and test for the other release targets; lint the Windows build (no cross-OS test run)
	GOOS=linux GOARCH=amd64 go vet ./...
	GOOS=windows GOARCH=amd64 go vet ./...
	GOOS=windows GOARCH=arm64 go vet ./...
	GOOS=windows golangci-lint run ./...

test-race: ## Unit tests under the race detector (slower; the mcpserver tests rebuild the child binary with -race)
	go test -race ./...

verify: ## Done gate: build, vet, vet-cross, gofmt, lint, tests, in that order
	$(MAKE) build
	$(MAKE) build-all
	$(MAKE) vet
	$(MAKE) vet-cross
	$(MAKE) fmt-check
	$(MAKE) lint
	$(MAKE) test
	@echo "verify: OK"

release-check: ## Release tooling checks: actionlint, shellcheck, release.sh unit tests, template rendering, install.sh in containers. Needs actionlint, shellcheck, Docker, python3, ruby, openssl@3; not part of verify
	actionlint .github/workflows/*.yml
	@if rg -n 'uses: .*@v[0-9]' .github/workflows/; then echo "error: unpinned action (use a commit SHA with the version in a comment)"; exit 1; fi
	shellcheck -x release/*.sh
	shellcheck -s sh install.sh
	@echo "note: release/test-install.ps1 runs on Windows only (windows.yml, release/test-windows.ps1)"
	release/test-release.sh
	release/test-render.sh
	release/test-install.sh
