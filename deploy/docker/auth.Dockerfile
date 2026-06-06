# Build from the REPO ROOT:
#   docker build -f deploy/docker/auth.Dockerfile -t logistics-auth .
#
# Each service module resolves the shared platform module via a relative replace
# directive, so we copy platform/ + the service and build with GOWORK=off.
FROM golang:1.25-bookworm AS build
WORKDIR /src
ENV GOWORK=off CGO_ENABLED=0 GOOS=linux

# Dependency layer: copy module manifests first for caching.
COPY platform/go.mod platform/go.sum ./platform/
COPY auth/go.mod auth/go.sum ./auth/
RUN cd auth && go mod download

# Source for the shared module + this service only.
COPY platform/ ./platform/
COPY auth/ ./auth/
RUN cd auth && go build -trimpath -ldflags="-s -w" -o /out/auth ./cmd/auth

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/auth /auth
COPY --from=build /src/auth/migrations /migrations
ENV MIGRATIONS_DIR=/migrations
EXPOSE 8001
USER nonroot:nonroot
ENTRYPOINT ["/auth"]
