.PHONY: build cli serve install test check check-race fmt-check test-web gateway-add

# The build version comes from the VERSION file. A checkout without one builds
# as "dev", and "dev" turns the update check off.
VERSION ?= $(or $(shell tr -d '[:space:]' < VERSION 2>/dev/null),dev)

build:
	mkdir -p ./bin
	go build -ldflags "-X main.version=$(VERSION)" -o bin/nusallm ./cmd/nusallm

cli: build
	./bin/nusallm
	
serve: build
	./bin/nusallm serve

install: build
	./bin/nusallm serve install

test: build
	go fix ./...
	go vet ./...
	go test ./...

# One read-only gate over the whole tree: formatting, the WebUI (syntax plus
# the projection tests under node --test), host build, vet, tests, and a
# cross-compile of every package for the platforms the release claims.
# Separate from `test` because `test` runs `go fix`, which rewrites sources —
# a check that mutates its subject cannot report on it. A failing platform
# names itself in the output; `GOOS=X go build ./...` leaves no binary behind
# because it covers more than one package.
check: fmt-check test-web
	go build ./...
	go vet ./...
	go test ./...
	GOOS=windows go build ./...
	GOOS=darwin go build ./...

# The concurrency gate: the race detector over the packages whose state is
# shared across goroutines — the socket hub (bounded queues, subscriber sets),
# the turn loop (retry, persistence ordering), and the workspace repository
# (per-call os.Root sessions). Deliberately NOT part of `check`: `check` stays
# fast and read-only, while -race rebuilds these packages with instrumentation.
# A reviewer closing a concurrency change runs both.
check-race:
	go test -count=1 -race ./infrastructure/delivery/websocket/... ./application/job/... ./infrastructure/repository/workspace/...

fmt-check:
	@files=$$(gofmt -l $$(go list -f '{{.Dir}}' ./... | grep -v /vendor/)); \
	if [ -n "$$files" ]; then echo "$$files"; exit 1; fi

# Syntax-checks the WebUI sources (vendored files excluded), then runs the
# projection tests under Node's built-in runner — no npm, no dependencies.
test-web:
	@for f in web/app.js web/lib/*.js web/components/*.js; do node --check "$$f" || exit 1; done
	node --import ./web/tests/register.mjs --test web/tests/*.test.mjs

gateway-add: build
	./bin/nusallm gateway add \
	opencode --style completion \
	--endpoint https://opencode.ai/zen/v1 \
	--models big-pickle --key-stdin
