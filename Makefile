.PHONY: build cli serve test gateway-add

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
	gofmt -w $$(go list -f '{{.Dir}}' ./... | grep -v /vendor/)

gateway-add: build
	./bin/nusallm gateway add \
	opencode --style completion \
	--endpoint https://opencode.ai/zen/v1 \
	--models space-bunny-free --key-stdin
