# syntax=docker/dockerfile:1

# Multi-stage build: the toolchain never ships to production, and the runtime
# image contains only the three binaries plus the certificates and time zone
# data they need.

# ---- build -------------------------------------------------------------------
FROM golang:1.26-alpine AS build

WORKDIR /src

# The module files are copied first so the dependency layer is cached
# independently of source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Recorded in the binary so /health reports which artefact is actually running.
# Defaults are deliberately non-committal; compose and CI pass real values.
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

ENV CGO_ENABLED=0 \
    GOOS=linux \
    LDFLAGS="-s -w \
      -X github.com/snail46/vps-billing-workbuddy/backend/internal/version.Version=${VERSION} \
      -X github.com/snail46/vps-billing-workbuddy/backend/internal/version.Commit=${COMMIT} \
      -X github.com/snail46/vps-billing-workbuddy/backend/internal/version.BuildTime=${BUILD_TIME}"

# -trimpath keeps absolute build paths out of the binary; with -s -w the result
# is a static, reproducible binary that runs on a bare base image.
RUN go build -trimpath -ldflags "${LDFLAGS}" -o /out/server ./cmd/server \
 && go build -trimpath -ldflags "${LDFLAGS}" -o /out/worker ./cmd/worker \
 && go build -trimpath -ldflags "${LDFLAGS}" -o /out/migrate ./cmd/migrate

# ---- runtime -----------------------------------------------------------------
FROM alpine:3.20 AS runtime

# ca-certificates: outbound TLS to payment gateways and providers.
# tzdata: the platform stores UTC but must be able to resolve named zones.
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S -g 10001 app \
 && adduser -S -u 10001 -G app -H -s /sbin/nologin app

WORKDIR /app
COPY --from=build /out/server /out/worker /out/migrate /app/

# Run unprivileged. The container needs no write access to its own filesystem
# and no capabilities, so a compromised process gains nothing from the host.
USER 10001:10001

# Documented only; the published port is set by the environment that runs it.
EXPOSE 8080

# CMD, deliberately not ENTRYPOINT. This image carries three binaries (server,
# worker, migrate) and the environment selects one per service through compose's
# `command`. Docker Compose *overrides* CMD with `command`, but it *appends*
# `command` to ENTRYPOINT — so with ENTRYPOINT ["/app/server"] the migrate service
# would silently run `/app/server /app/migrate up`: the API would start instead of
# the migration, never exit, and block every service waiting on
# `service_completed_successfully`. That is exactly how the Gate hung for 24
# minutes. CMD keeps the convenient `docker run` default while making `command`
# select the binary outright.
CMD ["/app/server"]
