# syntax=docker/dockerfile:1
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY api/openapi.yaml /src/api/openapi.yaml
COPY web/ ./
RUN npm run build

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X drool/internal/version.Version=${VERSION} -X drool/internal/version.Commit=${COMMIT} -X drool/internal/version.Date=${DATE}" \
      -o /out/drool ./cmd/drool \
 && for arch in amd64 arm64; do \
      CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
        -ldflags "-s -w -X drool/internal/version.Version=${VERSION} -X drool/internal/version.Commit=${COMMIT} -X drool/internal/version.Date=${DATE}" \
        -o /out/drool-agent-linux-$arch ./cmd/drool-agent; \
    done \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/drool /usr/local/bin/drool
# Agent binaries served to new hosts (agent.binaries_dir defaults to drool's directory).
COPY --from=build /out/drool-agent-linux-amd64 /out/drool-agent-linux-arm64 /usr/local/bin/
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV DROOL_DATA_DIR=/data \
    DROOL_LOGGING_OUTPUT=stdout
VOLUME /data
# Web UI/API and the agent endpoint (mutual TLS, agents connect outbound to it).
EXPOSE 8080 9443
USER nonroot
ENTRYPOINT ["/usr/local/bin/drool"]
CMD ["serve"]
