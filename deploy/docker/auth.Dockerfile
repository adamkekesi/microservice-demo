# Build from the REPO ROOT:
#   docker build -f deploy/docker/auth.Dockerfile -t logistics-auth .
#
# platform is a PUBLIC published module, so the build fetches it (and all deps)
# from the Go proxy — no credentials, no workspace, no secrets.
FROM golang:1.25-bookworm AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux

COPY auth/go.mod auth/go.sum ./auth/
RUN --mount=type=cache,target=/go/pkg/mod \
    cd auth && go mod download

COPY auth/ ./auth/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd auth && go build -trimpath -ldflags="-s -w" -o /out/auth ./cmd/auth

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/auth /auth
COPY --from=build /src/auth/migrations /migrations
ENV MIGRATIONS_DIR=/migrations
EXPOSE 8001
USER nonroot:nonroot
ENTRYPOINT ["/auth"]
