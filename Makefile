.PHONY: build cli serve install test fmt-check test-web e2e-web gateway-add

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
	go vet ./...
	go test ./...

fmt-check:
	@files=$$(gofmt -l $$(go list -f '{{.Dir}}' ./... | grep -v /vendor/)); \
	if [ -n "$$files" ]; then echo "$$files"; exit 1; fi

test-web:
	node --test web/test/*.test.mjs

# Headless browser suite; needs @playwright/test installed locally or
# PLAYWRIGHT_MODULE=/abs/path/to/@playwright/test/index.mjs, and a
# Chrome channel on PATH.
PLAYWRIGHT_MODULE ?= $(wildcard $(shell npm root -g 2>/dev/null)/@playwright/test/index.mjs)
export PLAYWRIGHT_MODULE
e2e-web:
	node web/test-e2e/steer-flow.mjs

gateway-add: build
	./bin/nusallm gateway add \
	opencode --style completion \
	--endpoint https://opencode.ai/zen/v1 \
	--models big-pickle --key-stdin
