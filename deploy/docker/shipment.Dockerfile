# Build from the REPO ROOT:
#   docker build -f deploy/docker/shipment.Dockerfile -t logistics-shipment .
# platform is a PUBLIC published module — fetched from the Go proxy, no creds.
FROM golang:1.25-bookworm AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux

COPY shipment/go.mod shipment/go.sum ./shipment/
RUN --mount=type=cache,target=/go/pkg/mod \
    cd shipment && go mod download

COPY shipment/ ./shipment/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd shipment && go build -trimpath -ldflags="-s -w" -o /out/shipment ./cmd/shipment

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/shipment /shipment
COPY --from=build /src/shipment/migrations /migrations
ENV MIGRATIONS_DIR=/migrations
EXPOSE 8003
USER nonroot:nonroot
ENTRYPOINT ["/shipment"]
