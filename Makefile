PKG      := github.com/TamerlanK/hearth/internal/cli

# Every stamp comes from git, not from the shell: mingw make on Windows runs
# recipes through cmd.exe, which has no date(1) and no /dev/null. DATE is the
# commit time, the same value Go's own VCS stamping records as vcs.time.
VERSION  ?= $(or $(shell git describe --tags --always --dirty),dev)
COMMIT   ?= $(or $(shell git rev-parse --short HEAD),none)
DATE     ?= $(or $(shell git log -1 --format=%cI),unknown)
LDFLAGS  := -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).Date=$(DATE)

ifeq ($(OS),Windows_NT)
EXE := .exe
endif
# Ask the shell what it is rather than trusting $(SHELL), which still reads
# /bin/sh when make is about to fall back to cmd.exe: only a POSIX shell
# strips the quotes. cmd.exe and sh delete a directory differently.
ifeq ($(shell echo 'x'),x)
RMDIR := rm -rf
else
RMDIR := rmdir /s /q
endif

BINARY := bin/hearth$(EXE)

.PHONY: build build-load test bench fuzz lint fmt vet check demo release-dry docker run-server run-client clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/hearth

build-load:
	go build -o bin/hearth-load$(EXE) ./cmd/hearth-load

test:
	go test -race -count=1 ./...

bench:
	go test -run '^$$' -bench . -benchmem ./...

demo: build
	vhs demo/demo.tape

fuzz:
	go test ./pkg/protocol -run '^$$' -fuzz FuzzJSONDecode -fuzztime 10s
	go test ./pkg/protocol -run '^$$' -fuzz FuzzTextDecode -fuzztime 10s

# Both need golangci-lint (see README: Development); its config carries the
# gofmt and goimports formatters, so fmt needs no separate tool.
lint:
	golangci-lint run

fmt:
	golangci-lint fmt --diff

vet:
	go vet ./...

check: fmt vet lint test

release-dry:
	goreleaser release --snapshot --clean

docker:
	docker build -t hearth:$(VERSION) --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) .

run-server:
	go run ./cmd/hearth serve

run-client:
	go run ./cmd/hearth connect localhost:4000 --plain

clean:
	-$(RMDIR) bin
