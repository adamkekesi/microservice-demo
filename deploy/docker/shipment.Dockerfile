# Build from the REPO ROOT (see auth.Dockerfile for the GitHub-token secret):
#   GH_TOKEN=$(gh auth token) docker build --secret id=gh_token,env=GH_TOKEN \
#     -f deploy/docker/shipment.Dockerfile -t logistics-shipment .
FROM golang:1.25-bookworm AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux GOPRIVATE=github.com/adamkekesi/*

COPY shipment/ ./shipment/
RUN --mount=type=secret,id=gh_token \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    git config --global credential.helper '!f(){ echo username=x-access-token; echo "password=$(cat /run/secrets/gh_token)"; };f' && \
    cd shipment && go build -trimpath -ldflags="-s -w" -o /out/shipment ./cmd/shipment

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/shipment /shipment
COPY --from=build /src/shipment/migrations /migrations
ENV MIGRATIONS_DIR=/migrations
EXPOSE 8003
USER nonroot:nonroot
ENTRYPOINT ["/shipment"]
