.PHONY: build cli serve test fmt-check test-web gateway-add

build:
	mkdir -p ./bin
	go build -o bin/nusallm ./cmd/nusallm

cli: build
	./bin/nusallm
	
serve: build
	./bin/nusallm serve

test: build
	go vet ./...
	go test ./...

fmt-check:
	@files=$$(gofmt -l $$(go list -f '{{.Dir}}' ./... | grep -v /vendor/)); \
	if [ -n "$$files" ]; then echo "$$files"; exit 1; fi

test-web:
	node --test web/test/*.test.mjs

gateway-add: build
	./bin/nusallm gateway add \
	opencode --style completion \
	--endpoint https://opencode.ai/zen/v1 \
	--models space-bunny-free --key-stdin
