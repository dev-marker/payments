# syntax=docker/dockerfile:1
# One Dockerfile for all five services: docker build --build-arg SERVICE=wallet .

# Stage 1 builds, with the whole Go toolchain. None of it ships.
FROM golang:1.26.5-alpine AS build
WORKDIR /src

# Modules first, in a step of their own: it runs again only when go.mod or
# go.sum change. The cache mount keeps modules between builds, so even then,
# only the new ones are downloaded.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# Then our code. The build cache mount means a rebuild compiles only the
# packages that changed. Static, with no paths from this machine, stripped of
# debug symbols, and stamped with its version.
COPY . .
ARG SERVICE
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/service ./cmd/${SERVICE}

# Stage 2 is what ships: our binary, CA certificates, time zones, and a user
# who isn't root. No shell, no package manager.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/service /service
USER nonroot:nonroot
ENTRYPOINT ["/service"]

# Docker asks the binary itself: there's no curl in here. Every second while
# the service starts up, then every ten.
HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --start-interval=1s \
    CMD ["/service", "-healthcheck"]
