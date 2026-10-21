SHELL := /bin/sh

export GOPATH := $(CURDIR)/.tmp/go
export GOCACHE := $(CURDIR)/.tmp/go/cache
export GOMODCACHE := $(CURDIR)/.tmp/go/mod
export GOTMPDIR := $(CURDIR)/.tmp/go/tmp
export TMPDIR := $(CURDIR)/.tmp/go/tmp
export UV_CACHE_DIR := $(CURDIR)/.tmp/uv
export UV_PROJECT_ENVIRONMENT := $(CURDIR)/.tmp/venv-mac-fixture
export UV_PYTHON_INSTALL_DIR := $(CURDIR)/.tmp/python

GO_COV_VERSION := v0.1.4-0.20260617124522-d6caf2759e33
IMAGE ?= d2r-server:dev

.PHONY: prepare fmt-check vet test test-commands coverage check build relay-build cross-build docker-test docker-build fixture-test native-probe-test

prepare:
	@mkdir -p .tmp/go/cache .tmp/go/mod .tmp/go/tmp bin

fmt-check:
	@unformatted=$$(gofmt -l cmd internal) || exit $$?; if [ -n "$$unformatted" ]; then printf '%s\n' "$$unformatted"; exit 1; fi

vet: prepare
	go vet ./...

test: prepare
	go test -race -count=1 ./...

test-commands: prepare
	go test -race -count=1 ./cmd/...

coverage: prepare
	go run github.com/codetreker/go-cov/cmd/go-cov@$(GO_COV_VERSION) --config .go-cov.toml --ci=true --race=true --coverprofile .tmp/coverage.out

check: fmt-check vet test-commands coverage

relay-build: prepare
	go build -trimpath -o bin/d2r-relay ./cmd/d2r-relay

build: relay-build
	go build -trimpath -o bin/d2r-server ./cmd/d2r-server
	go build -trimpath -o bin/d2r-inspect ./cmd/d2r-inspect
	go build -trimpath -o bin/d2r-trace ./cmd/d2r-trace

cross-build: prepare
	@mkdir -p .tmp/cross
	@for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -o .tmp/cross/d2r-server-linux-$$arch ./cmd/d2r-server || exit; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -o .tmp/cross/d2r-inspect-linux-$$arch ./cmd/d2r-inspect || exit; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -o .tmp/cross/d2r-trace-linux-$$arch ./cmd/d2r-trace || exit; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -o .tmp/cross/d2r-relay-linux-$$arch ./cmd/d2r-relay || exit; \
	done

docker-test:
	docker build --target test -t d2r-server-tests:dev .

docker-build:
	docker build -t $(IMAGE) .

fixture-test: prepare
	uv run --locked --python 3.12 --project tools/mac-fixture python -m unittest discover -s tools/mac-fixture/tests

native-probe-test: prepare
	native/transport-probe/tests/run.sh
