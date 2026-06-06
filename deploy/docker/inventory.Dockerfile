# Build from the REPO ROOT:
#   docker build -f deploy/docker/inventory.Dockerfile -t logistics-inventory .
FROM golang:1.25-bookworm AS build
WORKDIR /src
ENV GOWORK=off CGO_ENABLED=0 GOOS=linux

COPY platform/go.mod platform/go.sum ./platform/
COPY inventory/go.mod inventory/go.sum ./inventory/
RUN cd inventory && go mod download

COPY platform/ ./platform/
COPY inventory/ ./inventory/
RUN cd inventory && go build -trimpath -ldflags="-s -w" -o /out/inventory ./cmd/inventory

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/inventory /inventory
COPY --from=build /src/inventory/migrations /migrations
ENV MIGRATIONS_DIR=/migrations
EXPOSE 8002
USER nonroot:nonroot
ENTRYPOINT ["/inventory"]
