# Fleet Manager

Web-first Linux fleet management: SSH and agent-based management, automation,
and (later) intent-based network segmentation.

- **Design:** [`DESIGN.md`](DESIGN.md) (product and trust architecture)
- **Current implementation plan:** [`ARCHITECTURE-ALPHA.md`](ARCHITECTURE-ALPHA.md) (SSH-only Technical Alpha)

Status: **M0 (skeleton)**. Users, sessions, API tokens, audit log, config, health endpoints
and the web UI shell work. Hosts and jobs arrive in M1–M2.

## Requirements

- Go 1.27+
- Node 22+ and npm
- [sqlc](https://sqlc.dev) (only to regenerate query code: `go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1`)

## Build and run

```sh
make build                       # web UI + static binary at bin/fleetmgr

# Create the first administrator (prompts for a password, min. 12 characters).
FLEETMGR_DATA_DIR=$PWD/data ./bin/fleetmgr user create --username admin

make run                         # serves https://127.0.0.1:8080 with a self-signed cert
```

The first start generates a self-signed certificate and prints its SHA-256 fingerprint.
Compare it with what your browser shows.

### Frontend development

```sh
make run     # terminal 1: controller on https://127.0.0.1:8080
make dev     # terminal 2: Vite dev server with hot reload, proxying /api
```

## Configuration

Precedence: built-in defaults → config file (`--config`, default `/etc/fleetmgr/config.yaml`
if present) → `FLEETMGR_*` environment variables. The variable name is the key path in upper
snake case, e.g. `server.web_port` → `FLEETMGR_SERVER_WEB_PORT`. **Settings → System** in the UI
shows every effective value and where it came from.

TLS defaults to a self-signed certificate. `server.tls.mode: off` (for a reverse proxy on the
same host) requires a loopback `server.bind`.

## Development

```sh
make test              # Go tests + frontend typecheck
make vet fmt-check
make generate          # regenerate sqlc code and TypeScript API types
make check-generated   # CI: fail if generated code is stale
```

The REST contract lives in [`api/openapi.yaml`](api/openapi.yaml). A Go test fails if routes
and the spec drift apart. The UI's TypeScript client types are generated from the spec.

### Frontend styling

All colors, radii and fonts are semantic design tokens in `web/src/styles/index.css`, and
components use only those tokens through Tailwind utilities (`bg-surface`, `text-muted`,
`border-line`, ...). UI primitives live in `web/src/components/ui/`. Restyling means changing
tokens and primitives, not pages.
