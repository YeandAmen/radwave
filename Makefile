.PHONY: run crawl build test check browser-test

run:
	go run ./cmd/radar serve

crawl:
	go run ./cmd/radar crawl

build:
	mkdir -p dist
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/opportunity-radar ./cmd/radar

test:
	go test -race ./...

check:
	go vet ./...
	go test -race ./...

browser-test:
	npm run test:e2e
