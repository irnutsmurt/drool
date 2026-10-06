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
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X fleetmgr/internal/version.Version=${VERSION} -X fleetmgr/internal/version.Commit=${COMMIT}" \
      -o /out/fleetmgr ./cmd/fleetmgr \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/fleetmgr /usr/local/bin/fleetmgr
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV FLEETMGR_DATA_DIR=/data \
    FLEETMGR_LOGGING_OUTPUT=stdout
VOLUME /data
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/usr/local/bin/fleetmgr"]
CMD ["serve"]
