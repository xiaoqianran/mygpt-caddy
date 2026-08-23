.PHONY: build test check

build:
	go build -trimpath -ldflags='-s -w' -o bin/mygpt-caddy ./cmd/mygpt-caddy

test:
	go test ./...

check:
	gofmt -w cmd internal
	go vet ./...
	go test ./...

