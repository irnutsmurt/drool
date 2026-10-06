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
      -ldflags "-s -w -X drool/internal/version.Version=${VERSION} -X drool/internal/version.Commit=${COMMIT}" \
      -o /out/drool ./cmd/drool \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/drool /usr/local/bin/drool
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV DROOL_DATA_DIR=/data \
    DROOL_LOGGING_OUTPUT=stdout
VOLUME /data
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/usr/local/bin/drool"]
CMD ["serve"]
