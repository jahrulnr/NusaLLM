.PHONY: build cli serve install test fmt-check test-web gateway-add

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

fmt-check:
	@files=$$(gofmt -l $$(go list -f '{{.Dir}}' ./... | grep -v /vendor/)); \
	if [ -n "$$files" ]; then echo "$$files"; exit 1; fi

# Syntax-checks the WebUI sources (vendored files excluded).
test-web:
	@for f in web/app.js web/lib/*.js web/components/*.js; do node --check "$$f" || exit 1; done

gateway-add: build
	./bin/nusallm gateway add \
	opencode --style completion \
	--endpoint https://opencode.ai/zen/v1 \
	--models big-pickle --key-stdin
