SHELL := /bin/sh
GO ?= /Users/almirsarajcic/.local/share/mise/installs/go/1.27.1/bin/go
export GOTOOLCHAIN := local
export GOWORK := off
export GOFLAGS := -mod=vendor -p=2
export GOPROXY := off
export GOSUMDB := off
export GOMAXPROCS := 2

.PHONY: check fmt lint test build race clean
check:
	@KGO_GO="$(GO)" ./tools/check.sh
fmt:
	@find cmd internal tools -name '*.go' -type f -exec "$(dir $(GO))gofmt" -w {} +
lint:
	$(GO) vet ./...
test:
	$(GO) test -count=1 -parallel=2 ./...
build:
	@mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/kogen ./cmd/kogen
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/kogen-xspec ./cmd/kogen-xspec
race:
	CGO_ENABLED=1 $(GO) test -race -count=1 -parallel=2 ./...
clean:
	rm -rf bin
