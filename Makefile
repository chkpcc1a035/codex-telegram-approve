.PHONY: build test vet
build:
	mkdir -p bin
	go build -trimpath -o bin/codex-telegram-approve .
test:
	go test ./...
vet:
	go vet ./...
