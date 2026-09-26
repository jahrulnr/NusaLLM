build:
	mkdir -p ./bin
	go build -o bin/llm cmd/core/main.go

cli: build
	./bin/llm

gateway-add: build
	./bin/llm gateway add \
	opencode --style completion \
	--endpoint https://opencode.ai/zen/v1 \
	--models space-bunny-free --key-stdin
