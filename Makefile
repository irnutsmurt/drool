# Fleet Manager build. Requires Go 1.27+ and Node 22+.
GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X fleetmgr/internal/version.Version=$(VERSION) \
	-X fleetmgr/internal/version.Commit=$(COMMIT) \
	-X fleetmgr/internal/version.Date=$(DATE)

.PHONY: all build web web-deps go test test-go test-web vet fmt-check generate check-generated run dev clean

all: build

build: web go

web-deps:
	cd web && npm ci

web: web-deps
	cd web && npm run build

# Static binary (pure-Go SQLite, no cgo).
go:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/fleetmgr ./cmd/fleetmgr

test: test-go test-web

test-go:
	$(GO) test ./...

test-web:
	cd web && npm run typecheck

vet:
	$(GO) vet ./...

fmt-check:
	@out=$$(gofmt -l cmd internal web/embed.go); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

# Regenerate sqlc queries and TypeScript API types.
generate:
	sqlc generate
	cd web && npm run gen:api

# Fails if generated code is out of date (used in CI).
check-generated: generate
	git diff --exit-code -- internal/store/db web/src/api/schema.d.ts

# Run the controller locally with a throwaway data dir, bound to loopback.
run: go
	FLEETMGR_DATA_DIR=$(CURDIR)/data FLEETMGR_SERVER_BIND=127.0.0.1 FLEETMGR_LOGGING_OUTPUT=stdout \
		FLEETMGR_LOGGING_FORMAT=text ./bin/fleetmgr serve

# Frontend dev server with hot reload; proxies /api to `make run`.
dev:
	cd web && npm run dev

clean:
	rm -rf bin web/dist/assets web/dist/index.html
