BINARY   := bin/hearth
PKG      := github.com/TamerlanK/hearth/internal/cli
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).Date=$(DATE)

.PHONY: build test fuzz lint fmt vet check docker run-server run-client clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/hearth

test:
	go test -race -count=1 ./...

fuzz:
	go test ./pkg/protocol -run '^$$' -fuzz FuzzJSONDecode -fuzztime 10s
	go test ./pkg/protocol -run '^$$' -fuzz FuzzTextDecode -fuzztime 10s

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed; skipping (see README: Development)"; \
	fi

fmt:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@if command -v goimports >/dev/null 2>&1; then \
		out=$$(goimports -l .); if [ -n "$$out" ]; then echo "goimports needed:"; echo "$$out"; exit 1; fi; \
	fi

vet:
	go vet ./...

check: fmt vet lint test

docker:
	docker build -t hearth:$(VERSION) \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) .

run-server:
	go run ./cmd/hearth serve

run-client:
	go run ./cmd/hearth connect localhost:4000 --plain

clean:
	rm -rf bin
