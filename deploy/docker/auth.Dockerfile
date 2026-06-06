# Build from the REPO ROOT (platform is a PRIVATE published module, so the build
# needs a GitHub token passed as a BuildKit secret):
#   GH_TOKEN=$(gh auth token) docker build --secret id=gh_token,env=GH_TOKEN \
#     -f deploy/docker/auth.Dockerfile -t logistics-auth .
#
# The token is consumed by a git credential helper only while fetching the
# private module; it is never written into an image layer.
FROM golang:1.25-bookworm AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux GOPRIVATE=github.com/adamkekesi/*

COPY auth/ ./auth/
RUN --mount=type=secret,id=gh_token \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    git config --global credential.helper '!f(){ echo username=x-access-token; echo "password=$(cat /run/secrets/gh_token)"; };f' && \
    cd auth && go build -trimpath -ldflags="-s -w" -o /out/auth ./cmd/auth

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/auth /auth
COPY --from=build /src/auth/migrations /migrations
ENV MIGRATIONS_DIR=/migrations
EXPOSE 8001
USER nonroot:nonroot
ENTRYPOINT ["/auth"]
