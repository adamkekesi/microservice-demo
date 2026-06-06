# Build from the REPO ROOT (see auth.Dockerfile for the GitHub-token secret):
#   GH_TOKEN=$(gh auth token) docker build --secret id=gh_token,env=GH_TOKEN \
#     -f deploy/docker/inventory.Dockerfile -t logistics-inventory .
FROM golang:1.25-bookworm AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux GOPRIVATE=github.com/adamkekesi/*

COPY inventory/ ./inventory/
RUN --mount=type=secret,id=gh_token \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    git config --global credential.helper '!f(){ echo username=x-access-token; echo "password=$(cat /run/secrets/gh_token)"; };f' && \
    cd inventory && go build -trimpath -ldflags="-s -w" -o /out/inventory ./cmd/inventory

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/inventory /inventory
COPY --from=build /src/inventory/migrations /migrations
ENV MIGRATIONS_DIR=/migrations
EXPOSE 8002
USER nonroot:nonroot
ENTRYPOINT ["/inventory"]
