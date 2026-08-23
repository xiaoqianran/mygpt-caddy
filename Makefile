.PHONY: build test check

build:
	go build -trimpath -ldflags='-s -w' -o bin/mygpt-caddy ./cmd/mygpt-caddy
	go build -trimpath -ldflags='-s -w' -o bin/mygpt-audit ./cmd/mygpt-audit

test:
	go test ./...

check:
	gofmt -w cmd internal
	go vet ./...
	go test ./...
