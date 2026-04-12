GO_CACHE_DIR := $(CURDIR)/.cache/go-build
GO_MOD_CACHE_DIR := $(CURDIR)/.cache/go-mod
GO_PATH_DIR := $(CURDIR)/.cache/go

.PHONY: run
run:
	mkdir -p "$(GO_CACHE_DIR)" "$(GO_MOD_CACHE_DIR)" "$(GO_PATH_DIR)"
	GOCACHE="$(GO_CACHE_DIR)" GOMODCACHE="$(GO_MOD_CACHE_DIR)" GOPATH="$(GO_PATH_DIR)" go run ./cmd/codewithphone start

.PHONY: build
build:
	mkdir -p "$(GO_CACHE_DIR)" "$(GO_MOD_CACHE_DIR)" "$(GO_PATH_DIR)"
	GOCACHE="$(GO_CACHE_DIR)" GOMODCACHE="$(GO_MOD_CACHE_DIR)" GOPATH="$(GO_PATH_DIR)" go build -o bin/codewithphone ./cmd/codewithphone

.PHONY: test
test:
	mkdir -p "$(GO_CACHE_DIR)" "$(GO_MOD_CACHE_DIR)" "$(GO_PATH_DIR)"
	GOCACHE="$(GO_CACHE_DIR)" GOMODCACHE="$(GO_MOD_CACHE_DIR)" GOPATH="$(GO_PATH_DIR)" go test ./...
