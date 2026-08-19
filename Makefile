BINARY := san
BINDIR := bin
SRCDIR := ./cmd/san
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILDTIME := $(shell date -u +%Y-%m-%d)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION) -X main.buildTime=$(BUILDTIME) -X main.commit=$(COMMIT)"

# Disable cgo so binaries are statically linked, with no glibc version
# dependency that would break on older distros.
export CGO_ENABLED := 0
GOFILES := $(shell find . -path './vendor' -prune -o -path './.git' -prune -o -name '*.go' -print)
GOIMPORTS_VERSION := v0.43.0
GOLANGCI_VERSION := v2.12.2
GOVULNCHECK_VERSION := v1.7.0

.PHONY: build build-all install clean release release-push test cover format format-check lint lint-go install-format-tools install-lint-tools check-format-tools check-lint-tools

build: format
	@mkdir -p $(BINDIR)
	go build $(LDFLAGS) -o $(BINDIR)/$(BINARY) $(SRCDIR)

build-all: format
	go build ./...

install: build
	@mkdir -p $(HOME)/.local/bin
	@# Remove the old binary first so cp writes a fresh inode. Overwriting in
	@# place reuses the inode, whose cached code signature macOS (AMFI) then
	@# rejects on the new bytes ("code signature invalid") → SIGKILL on launch.
	@rm -f $(HOME)/.local/bin/$(BINARY)
	cp $(BINDIR)/$(BINARY) $(HOME)/.local/bin/

install-format-tools:
	go install golang.org/x/tools/cmd/goimports@$(GOIMPORTS_VERSION)

install-lint-tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

check-format-tools:
	@command -v goimports >/dev/null || go install golang.org/x/tools/cmd/goimports@$(GOIMPORTS_VERSION)

check-lint-tools:
	@command -v golangci-lint >/dev/null || go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

format: check-format-tools
	@gofmt -w $(GOFILES)
	@goimports -w $(GOFILES)

format-check: check-format-tools
	@files="$$(gofmt -l $(GOFILES))"; \
	if [ -n "$$files" ]; then \
		echo "Go files are not formatted. Run: make format"; \
		echo "$$files"; \
		exit 1; \
	fi
	@files="$$(goimports -l $(GOFILES))"; \
	if [ -n "$$files" ]; then \
		echo "Go imports are not formatted. Run: make format"; \
		echo "$$files"; \
		exit 1; \
	fi

lint:
	go vet ./...
	@$(MAKE) format-check
	@$(MAKE) lint-go
	@$(MAKE) lint-layers

# lint-go runs the linters configured in .golangci.yml. They report defects, not
# style, so the tree is expected to be clean — see the config for the rule on
# adding one.
lint-go: check-lint-tools
	golangci-lint run --timeout 5m ./...

lint-layers:
	@go run ./tools/layercheck

test:
	go test ./...

# cover runs the unit and integration tests with the race detector and writes a
# single merged coverage profile (coverage.out).
# -coverpkg=./internal/... attributes coverage to the internal packages from
# both suites, so end-to-end paths exercised only by the integration tests are
# counted too (a plain `./internal/...` run drops ~6 points of real coverage).
# covermode=atomic is required when -race is enabled. The race detector needs
# cgo, so override the global CGO_ENABLED=0 here; this only affects the
# ephemeral test binaries, not the statically linked release builds. -timeout
# bounds a hung test to 2 minutes (no package legitimately runs that long) so a
# deadlock fails fast instead of burning the 10-minute default.
cover:
	CGO_ENABLED=1 go test -race -covermode=atomic -timeout 120s \
		-coverpkg=./internal/... -coverprofile=coverage.out \
		./internal/... ./tests/integration/...

# ci runs everything the GitHub workflow runs, in the same order. Use
# `make ci` before pushing to catch format / vet / layercheck / test
# failures locally instead of round-tripping through Actions. `cover` already
# runs the integration tests (with coverage), so they need no separate step.
ci: format-check build-all lint
	$(MAKE) cover

clean:
	rm -rf $(BINDIR)

# vulncheck reports known vulnerabilities in the dependency graph that this
# code can actually reach. Reachability is what makes it worth gating on: an
# advisory in a package nobody calls is not reported, so a finding here is
# always a real exposure. CI runs the same command.
.PHONY: vulncheck
vulncheck:
	@command -v govulncheck >/dev/null || go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	govulncheck ./...

# Serve the website locally for testing. Opens http://localhost:8080 in the
# default browser. Requires Python 3 (available on macOS and most Linux distros
# by default). Stop with Ctrl-C.
.PHONY: serve-site
serve-site:
	@echo "→ Starting local server at http://localhost:8080"
	@echo "  Press Ctrl-C to stop."
	@echo ""
	@open http://localhost:8080 2>/dev/null || true
	python3 -m http.server 8080 -d site/

release: format
	@mkdir -p $(BINDIR)
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BINDIR)/$(BINARY)_darwin_amd64 $(SRCDIR)
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BINDIR)/$(BINARY)_darwin_arm64 $(SRCDIR)
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BINDIR)/$(BINARY)_linux_amd64 $(SRCDIR)
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(BINDIR)/$(BINARY)_linux_arm64 $(SRCDIR)
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(BINDIR)/$(BINARY)_windows_amd64.exe $(SRCDIR)
	GOOS=windows GOARCH=arm64 go build $(LDFLAGS) -o $(BINDIR)/$(BINARY)_windows_arm64.exe $(SRCDIR)
	cd $(BINDIR) && cp $(BINARY)_darwin_amd64 $(BINARY) && tar -czf $(BINARY)_darwin_amd64.tar.gz $(BINARY) && rm $(BINARY)
	cd $(BINDIR) && cp $(BINARY)_darwin_arm64 $(BINARY) && tar -czf $(BINARY)_darwin_arm64.tar.gz $(BINARY) && rm $(BINARY)
	cd $(BINDIR) && cp $(BINARY)_linux_amd64 $(BINARY) && tar -czf $(BINARY)_linux_amd64.tar.gz $(BINARY) && rm $(BINARY)
	cd $(BINDIR) && cp $(BINARY)_linux_arm64 $(BINARY) && tar -czf $(BINARY)_linux_arm64.tar.gz $(BINARY) && rm $(BINARY)
	cd $(BINDIR) && cp $(BINARY)_windows_amd64.exe $(BINARY).exe && zip $(BINARY)_windows_amd64.zip $(BINARY).exe && rm $(BINARY).exe
	cd $(BINDIR) && cp $(BINARY)_windows_arm64.exe $(BINARY).exe && zip $(BINARY)_windows_arm64.zip $(BINARY).exe && rm $(BINARY).exe

release-push:
	@test -n "$(VERSION)" || { echo "VERSION is required, e.g. make release-push VERSION=v1.15.2"; exit 1; }
	@case "$(VERSION)" in v*) tag="$(VERSION)" ;; *) tag="v$(VERSION)" ;; esac; \
	git diff --quiet || { echo "working tree is not clean"; exit 1; }; \
	git diff --cached --quiet || { echo "index has staged changes"; exit 1; }; \
	git rev-parse --verify "$$tag" >/dev/null 2>&1 && { echo "tag $$tag already exists"; exit 1; }; \
	grep -q "^## \[$$tag\]" CHANGELOG.md || { echo "CHANGELOG.md is missing section $$tag"; exit 1; }; \
	git push origin main && git tag "$$tag" && git push origin "$$tag"
