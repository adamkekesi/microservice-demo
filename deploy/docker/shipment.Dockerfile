# Build from the REPO ROOT:
#   docker build -f deploy/docker/shipment.Dockerfile -t logistics-shipment .
FROM golang:1.25-bookworm AS build
WORKDIR /src
ENV GOWORK=off CGO_ENABLED=0 GOOS=linux

COPY platform/go.mod platform/go.sum ./platform/
COPY shipment/go.mod shipment/go.sum ./shipment/
RUN cd shipment && go mod download

COPY platform/ ./platform/
COPY shipment/ ./shipment/
RUN cd shipment && go build -trimpath -ldflags="-s -w" -o /out/shipment ./cmd/shipment

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/shipment /shipment
COPY --from=build /src/shipment/migrations /migrations
ENV MIGRATIONS_DIR=/migrations
EXPOSE 8003
USER nonroot:nonroot
ENTRYPOINT ["/shipment"]
