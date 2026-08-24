# syntax=docker/dockerfile:1.7

ARG GO_IMAGE=golang:1.22.12-alpine3.21@sha256:1699c10032ca2582ec89a24a1312d986a3f094aed3d5c1147b19880afe40e052
ARG NODE_IMAGE=node:22.14.0-alpine3.21@sha256:9bef0ef1e268f60627da9ba7d7605e8831d5b56ad07487d24d1aa386336d1944
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35

FROM ${GO_IMAGE} AS go-base
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

FROM go-base AS runner-build
ARG BUILD_VERSION=dev
ARG BUILD_REVISION=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X github.com/AreaSong/ops/services/areasong-ops/internal/buildinfo.Version=${BUILD_VERSION} -X github.com/AreaSong/ops/services/areasong-ops/internal/buildinfo.Revision=${BUILD_REVISION}" \
    -o /out/areasong-ops-runner ./cmd/runner
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w" \
    -o /out/areasong-ops-runner-updater ./cmd/runner-updater

FROM ${NODE_IMAGE} AS web-assets
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM go-base AS web-build
ARG BUILD_VERSION=dev
ARG BUILD_REVISION=unknown
COPY --from=web-assets /web/dist/ ./cmd/web/static/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X github.com/AreaSong/ops/services/areasong-ops/internal/buildinfo.Version=${BUILD_VERSION} -X github.com/AreaSong/ops/services/areasong-ops/internal/buildinfo.Revision=${BUILD_REVISION}" \
    -o /out/areasong-ops-web ./cmd/web

FROM scratch AS runner-export
COPY --from=runner-build /out/areasong-ops-runner /areasong-ops-runner
COPY --from=runner-build /out/areasong-ops-runner-updater /areasong-ops-runner-updater

FROM ${RUNTIME_IMAGE} AS web
ARG BUILD_VERSION=dev
ARG BUILD_REVISION=unknown
LABEL org.opencontainers.image.title="AreaSong Ops Web" \
      org.opencontainers.image.version="${BUILD_VERSION}" \
      org.opencontainers.image.revision="${BUILD_REVISION}"
WORKDIR /app
COPY --from=web-build --chown=nonroot:nonroot /out/areasong-ops-web /app/areasong-ops-web
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app/areasong-ops-web"]
