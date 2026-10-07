# DROOL build. Requires Go 1.27+ and Node 22+.
GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X drool/internal/version.Version=$(VERSION) \
	-X drool/internal/version.Commit=$(COMMIT) \
	-X drool/internal/version.Date=$(DATE)

.PHONY: all build image web web-deps go agent proto test test-go test-web test-integration vet fmt-check generate check-generated run dev clean

all: build

build: web go

web-deps:
	cd web && npm ci

web: web-deps
	cd web && npm run build

# Static binaries (pure-Go SQLite, no cgo). The agent binaries sit next to
# drool, which serves them to new hosts (agent.binaries_dir).
go: agent
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/drool ./cmd/drool

agent:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/drool-agent-linux-amd64 ./cmd/drool-agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/drool-agent-linux-arm64 ./cmd/drool-agent

# Regenerate the agent protocol code (needs protoc, protoc-gen-go, protoc-gen-go-grpc).
proto:
	protoc -I api/agent/v1 --go_out=internal/agentrpc/pb --go_opt=paths=source_relative \
	  --go-grpc_out=internal/agentrpc/pb --go-grpc_opt=paths=source_relative api/agent/v1/agent.proto

test: test-go test-web

test-go:
	$(GO) test ./...

# Container image tagged with the version (drool:<version> and drool:local).
image:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) \
		-t drool:$(VERSION) -t drool:local .

# Real distributions in unprivileged containers (needs Docker and network
# access; ~15-30 min). DISTROS=debian-12,fedora-44 limits the matrix.
test-integration:
	go test -tags integration -timeout 90m -count=1 ./test/integration/ -v $(if $(DISTROS),-distros $(DISTROS))

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
	DROOL_DATA_DIR=$(CURDIR)/data DROOL_SERVER_BIND=127.0.0.1 DROOL_LOGGING_OUTPUT=stdout \
		DROOL_LOGGING_FORMAT=text ./bin/drool serve

# Frontend dev server with hot reload; proxies /api to `make run`.
dev:
	cd web && npm run dev

clean:
	rm -rf bin web/dist/assets web/dist/index.html
