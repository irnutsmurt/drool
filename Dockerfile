# DROOL's build: the web UI, the static controller and Shepherd binaries,
# the third-party notices and the container image, from one source tree.
#
#   make image                       this machine's platform, tagged drool:local
#   packaging/release/build.sh       every release artifact (RELEASING.md)
#
# It needs BuildKit (docker buildx). The release pipeline builds it for
# linux/amd64 and linux/arm64 on one machine: everything compiles on the
# build platform (Go cross-compiles), and the final stage only copies files,
# so no emulation is involved. Toolchains and the base image are pinned by
# digest, and the same commit gives the same bytes (SOURCE_DATE_EPOCH).
ARG NODE_IMAGE=node:22-bookworm-slim@sha256:c3de60bf2f9dd0ac6370e6117950ff62d6e339527e7472301c9c78a017978392
ARG GO_IMAGE=golang:1.27.1-bookworm@sha256:8d48e12ec56735e9358640898b9d9b9fcca110612ed8a5567438c0a1baa24e66
ARG BASE_IMAGE=gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3

FROM --platform=$BUILDPLATFORM ${NODE_IMAGE} AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund --loglevel=error
COPY api/openapi.yaml /src/api/openapi.yaml
COPY web/ ./
COPY packaging/release/npm-licenses.mjs /tmp/npm-licenses.mjs
RUN npm run build && node /tmp/npm-licenses.mjs . > /src/web-packages.json

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS source
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# With .git in the context, Go records the exact revision and whether the
# tree had uncommitted changes in every binary (drool version).
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=v0.0.0-dev
ARG COMMIT=unknown
ARG DATE=unknown
ARG CHANNEL=development
ENV CGO_ENABLED=0 GOOS=linux \
    LDFLAGS="-s -w -X drool/internal/version.Version=${VERSION} -X drool/internal/version.Commit=${COMMIT} -X drool/internal/version.Date=${DATE} -X drool/internal/version.Channel=${CHANNEL}"

# Both Shepherd binaries go into every image and archive: a controller hands
# each new host the one for its architecture.
FROM source AS agents
RUN for arch in amd64 arm64; do \
      GOARCH=$arch go build -trimpath -ldflags "$LDFLAGS" -o /out/drool-agent-linux-$arch ./cmd/drool-agent || exit 1; \
    done

FROM source AS controller
ARG TARGETARCH
RUN GOARCH=${TARGETARCH} go build -trimpath -ldflags "$LDFLAGS" -o /out/drool ./cmd/drool && mkdir -p /out/data

FROM source AS notices
COPY --from=web /src/web-packages.json /tmp/web-packages.json
COPY --from=agents /out/ /bins/
COPY --from=controller /out/drool /bins/drool
RUN mkdir -p /out/doc \
 && go run ./packaging/release/relkit notices -npm /tmp/web-packages.json -out /out/doc/THIRD_PARTY_NOTICES.txt \
      /bins/drool /bins/drool-agent-linux-amd64 /bins/drool-agent-linux-arm64 \
 && if [ -f LICENSE ]; then cp LICENSE /out/doc/LICENSE; fi

# What the release archives and packages are made of (one directory per
# platform): docker buildx build --target artifacts --output type=local,...
FROM scratch AS artifacts
COPY --from=controller /out/drool /drool
COPY --from=agents /out/ /
COPY --from=notices /out/doc/ /

FROM ${BASE_IMAGE}
ARG VERSION=v0.0.0-dev
ARG COMMIT=unknown
ARG DATE=unknown
LABEL org.opencontainers.image.title="DROOL" \
      org.opencontainers.image.description="DROOL controller: manage, automate and monitor Linux hosts over SSH or the DROOL agent" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${DATE}"
COPY --from=controller /out/drool /usr/local/bin/drool
# Shepherd binaries served to new hosts (agent.binaries_dir: next to drool).
COPY --from=agents /out/drool-agent-linux-amd64 /out/drool-agent-linux-arm64 /usr/local/bin/
COPY --from=notices /out/doc/ /usr/share/doc/drool/
COPY --from=controller --chown=65532:65532 /out/data /data
ENV DROOL_DATA_DIR=/data \
    DROOL_LOGGING_OUTPUT=stdout
VOLUME /data
# Web UI/API and the Shepherd endpoint (mutual TLS; Shepherds connect to it).
EXPOSE 8080 9443
USER nonroot
HEALTHCHECK --interval=30s --timeout=10s --start-period=60s --retries=3 CMD ["/usr/local/bin/drool", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/drool"]
CMD ["serve"]
