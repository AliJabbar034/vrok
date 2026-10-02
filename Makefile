# vrok — build, test and release tasks.

BINARY      := vrok
RELAY       := vrok-relay
BIN_DIR     := bin
DIST_DIR    := dist

# Every platform a release ships. CGO is off everywhere, so these are all
# cross-compiled from one machine with no toolchain to install.
PLATFORMS := \
	darwin/amd64 darwin/arm64 \
	linux/amd64 linux/arm64 linux/arm \
	windows/amd64 windows/arm64 \
	freebsd/amd64
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Build metadata is injected rather than compiled in, so `vrok --version`
# identifies the exact commit without the source having to know it.
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

# -trimpath keeps absolute build paths out of the binary. -buildvcs=false
# because the commit is already injected above, so Go's own stamping adds
# nothing and would otherwise fail to build from a source tarball.
GOFLAGS := -trimpath -buildvcs=false

# Prettier formats the files gofmt does not: docs, workflows, CSS.
#
# A locally installed copy is preferred, then one on PATH, then npx, which
# needs no install at all. Contributors without node are not blocked: the Go
# formatting still runs and CI reports anything missed.
PRETTIER := $(shell \
	if [ -x node_modules/.bin/prettier ]; then echo node_modules/.bin/prettier; \
	elif command -v prettier >/dev/null 2>&1; then echo prettier; \
	elif command -v npx >/dev/null 2>&1; then echo "npx --yes prettier@3"; \
	else echo ":"; fi)
HAVE_PRETTIER := [ "$(PRETTIER)" != ":" ]

GO_FILES := $(shell find . -name '*.go' -not -path './vendor/*')

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build both binaries into ./bin
	@mkdir -p $(BIN_DIR)
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) ./cmd/vrok
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(RELAY) ./cmd/vrok-relay
	@echo "built $(BIN_DIR)/$(BINARY) and $(BIN_DIR)/$(RELAY) ($(VERSION))"

.PHONY: install
install: ## Install vrok into GOPATH/bin
	go install $(GOFLAGS) -ldflags '$(LDFLAGS)' ./cmd/vrok

.PHONY: run
run: ## Run vrok with ARGS="./file --ttl 5m"
	go run ./cmd/vrok $(ARGS)

.PHONY: relay
relay: ## Run a local relay on :8787 for vrok.test
	go run ./cmd/vrok-relay -domain vrok.test -addr :8787 -scheme http -verbose

.PHONY: test
test: ## Run the test suite
	go test ./...

.PHONY: race
race: ## Run the test suite under the race detector
	go test -race -count=1 ./...

.PHONY: cover
cover: ## Report coverage and write coverage.html
	go test -coverprofile=coverage.out -covermode=atomic ./...
	@go tool cover -func=coverage.out | tail -1
	@go tool cover -html=coverage.out -o coverage.html
	@echo "wrote coverage.html"

.PHONY: fmt
fmt: ## Format everything: Go, plus docs and config
	gofmt -w $(GO_FILES)
	@$(PRETTIER) --write . >/dev/null 2>&1 && echo "prettier: formatted" \
		|| echo "prettier: skipped (install node, or run 'make fmt' again later)"

.PHONY: fmt-check
fmt-check: ## Fail if anything is unformatted
	@unformatted=$$(gofmt -l $(GO_FILES)); \
	if [ -n "$$unformatted" ]; then \
		echo "these files need gofmt:"; echo "$$unformatted"; exit 1; \
	fi
	@if $(HAVE_PRETTIER); then \
		$(PRETTIER) --check . || { echo "run: make fmt"; exit 1; }; \
	else \
		echo "prettier: skipped (no node available)"; \
	fi

.PHONY: hooks
hooks: ## Install the git hooks (format, commit message, branch name)
	@git rev-parse --git-dir >/dev/null 2>&1 \
		|| { echo "not a git repository yet; run 'git init' first"; exit 1; }
	@chmod +x .githooks/*
	@git config core.hooksPath .githooks
	@echo "git hooks enabled from .githooks/"
	@echo "  pre-commit   formats staged files, runs go vet"
	@echo "  commit-msg   requires a conventional commit subject"
	@echo "  pre-push     checks the branch name, runs vet and tests"
	@echo ""
	@echo "to skip once:  git commit --no-verify  /  git push --no-verify"

.PHONY: unhooks
unhooks: ## Turn the git hooks back off
	@git config --unset core.hooksPath 2>/dev/null || true
	@echo "git hooks disabled"

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: check
check: fmt-check vet build test ## Everything CI runs

.PHONY: tidy
tidy: ## Tidy go.mod
	go mod tidy

.PHONY: cross
cross: ## Check that every released platform still compiles
	@for t in $(PLATFORMS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		printf '  %-16s ' "$$t"; \
		if GOOS=$$os GOARCH=$$arch go vet ./... 2>/dev/null; then echo ok; \
		else echo FAIL; exit 1; fi; \
	done

.PHONY: completions
completions: ## Generate shell completions from the binary itself
	@mkdir -p completions
	@go run $(GOFLAGS) ./cmd/vrok completion bash > completions/vrok.bash
	@go run $(GOFLAGS) ./cmd/vrok completion zsh  > completions/vrok.zsh
	@go run $(GOFLAGS) ./cmd/vrok completion fish > completions/vrok.fish

# Archives are flat and CLI-only, matching exactly what .goreleaser.yaml
# publishes, so install.sh can be tested against a local `make dist`.
.PHONY: dist
dist: completions ## Build release archives and checksums for every platform
	@rm -rf $(DIST_DIR) && mkdir -p $(DIST_DIR)
	@for t in $(PLATFORMS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		ext=""; [ "$$os" = windows ] && ext=".exe"; \
		name="vrok_$(VERSION)_$${os}_$${arch}"; \
		stage="$(DIST_DIR)/.stage"; \
		rm -rf "$$stage" && mkdir -p "$$stage/completions"; \
		echo "  building $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build $(GOFLAGS) \
			-ldflags '$(LDFLAGS)' -o "$$stage/$(BINARY)$$ext" ./cmd/vrok || exit 1; \
		cp README.md LICENSE "$$stage/"; \
		cp completions/* "$$stage/completions/"; \
		if [ "$$os" = windows ]; then \
			(cd "$$stage" && zip -qr "../$$name.zip" .); \
		else \
			(cd "$$stage" && tar czf "../$$name.tar.gz" .); \
		fi; \
		rm -rf "$$stage"; \
	done
	@cd $(DIST_DIR) && (shasum -a 256 * 2>/dev/null || sha256sum *) > checksums.txt
	@echo
	@ls -1 $(DIST_DIR)

.PHONY: docker
docker: ## Build the relay image
	docker build -t vrok-relay:$(VERSION) .

.PHONY: clean
clean: ## Remove build artefacts
	rm -rf $(BIN_DIR) $(DIST_DIR) completions coverage.out coverage.html
