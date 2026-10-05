# syntax=docker/dockerfile:1

# Build the API binary, then ship it on a minimal base image.
#
# Migrations are embedded in the binary, so the runtime image needs neither the
# SQL files nor a migration tool.

FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies are copied and downloaded first so that a change to application
# code reuses the cached module layer instead of re-resolving every dependency.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static linking keeps the binary independent of the libc in the runtime image.
# CGO is disabled so the result also runs on a distroless or scratch base.
ARG VERSION=dev
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags "-s -w \
        -X main.version=${VERSION} \
        -X main.buildDate=${BUILD_DATE}" \
      -o /out/api \
      ./cmd/api

# Run a vet pass in the build so a container can never be produced from a tree
# that does not pass it.
RUN go vet ./...


FROM gcr.io/distroless/static-debian12:nonroot

# Certificates are needed to verify the JWKS endpoint and any HTTPS dependency.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/api /usr/local/bin/api

# The config file is mounted or supplied through the environment in a real
# deployment; this copy is the built-in default.
COPY --from=build /src/configs/config.yaml /etc/edp/config.yaml

# distroless "nonroot" is uid/gid 65532.
USER 65532:65532

EXPOSE 8080

# The orchestrator should restart the process if it stops answering, but should
# NOT restart it merely because a dependency is down: liveness stays green
# through a database outage and only readiness turns red.
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/api", "-healthcheck"]

ENTRYPOINT ["/usr/local/bin/api"]
CMD ["-config", "/etc/edp/config.yaml"]