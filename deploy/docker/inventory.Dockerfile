# Build from the REPO ROOT:
#   docker build -f deploy/docker/inventory.Dockerfile -t logistics-inventory .
# platform is a PUBLIC published module — fetched from the Go proxy, no creds.
FROM golang:1.25-bookworm AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux

COPY inventory/go.mod inventory/go.sum ./inventory/
RUN --mount=type=cache,target=/go/pkg/mod \
    cd inventory && go mod download

COPY inventory/ ./inventory/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd inventory && go build -trimpath -ldflags="-s -w" -o /out/inventory ./cmd/inventory

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/inventory /inventory
COPY --from=build /src/inventory/migrations /migrations
ENV MIGRATIONS_DIR=/migrations
EXPOSE 8002
USER nonroot:nonroot
ENTRYPOINT ["/inventory"]
