# DROOL build. Requires Go 1.27+ and Node 22+; images and releases need
# Docker (BuildKit: the buildx plugin, or packaging/release/buildx fetches a
# pinned standalone buildx). Release artifacts: make release (RELEASING.md).
GO      ?= go
VERSION ?= $(shell git describe --tags --match 'v[0-9]*' --always --dirty 2>/dev/null || echo v0.0.0-dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X drool/internal/version.Version=$(VERSION) \
	-X drool/internal/version.Commit=$(COMMIT) \
	-X drool/internal/version.Date=$(DATE)

.PHONY: all build image release web web-deps go agent proto test test-go test-race test-web test-integration vet fmt-check generate check-generated run dev clean

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

# The race detector slows internal/api to about ten minutes on a busy
# machine: Go's default per-package limit (10m) would call that a hang.
test-race:
	$(GO) test -race -count=1 -timeout 30m ./...

# Container image for this machine's platform, tagged drool:local (what
# compose.yaml uses by default). Development builds; releases: make release.
image:
	packaging/release/buildx build --load --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		--build-arg DATE=$(DATE) -t drool:local .

# Every release artifact for linux/amd64 and linux/arm64 from a clean clone of
# HEAD (or RELEASE_ARGS='--commit REV --version vX.Y.Z-rc.N ...'): dist/<version>/.
release:
	packaging/release/build.sh $(RELEASE_ARGS)

# Real distributions in unprivileged containers (needs Docker and network
# access; ~30-45 min). DISTROS=debian-12,fedora-44 limits the matrix;
# RELEASE=dist/<version> qualifies that release's artifacts (RELEASING.md).
test-integration:
	go test -tags integration -timeout 120m -count=1 ./test/integration/ -v $(if $(DISTROS),-distros $(DISTROS)) $(if $(RELEASE),-release $(RELEASE))

test-web:
	cd web && npm run typecheck && npm test

vet:
	$(GO) vet ./...

fmt-check:
	@out=$$(gofmt -l cmd internal packaging test web/embed.go); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

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
	rm -rf bin web/dist/assets web/dist/index.html dist
