build:
	mkdir -p ./bin
	go build -o bin/nusallm ./cmd/nusallm

cli: build
	./bin/nusallm

gateway-add: build
	./bin/nusallm gateway add \
	opencode --style completion \
	--endpoint https://opencode.ai/zen/v1 \
	--models space-bunny-free --key-stdin
