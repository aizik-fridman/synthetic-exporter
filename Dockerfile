# ─────────────────────────────────────────────────────────────────────────────
# Multi-stage Dockerfile for the All-in-One Synthetic Exporter
# ─────────────────────────────────────────────────────────────────────────────
#
# Stage 1 — Builder
#   Uses the official Go toolchain to compile a statically linked binary.
#
# Stage 2 — Runner
#   Uses the official Microsoft Playwright image so Chromium and all system
#   dependencies are pre-installed.  dumb-init is added as PID 1 to properly
#   reap zombie Chrome/Chromium child processes.
# ─────────────────────────────────────────────────────────────────────────────

# ── Stage 1: Build ────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

# Install git (required by `go mod download` for VCS-based modules).
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /src

# Cache dependency downloads separately from the build layer.
COPY go.mod go.sum ./
RUN go mod download

# Copy source and compile.
COPY . .

# CGO_ENABLED=0 produces a fully static binary that runs in a distro-less or
# minimal container without glibc.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
      -trimpath \
      -ldflags="-s -w -extldflags '-static'" \
      -o /bin/synthetic-exporter \
      ./main.go


# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
# mcr.microsoft.com/playwright ships Chromium and all OS-level dependencies
# (fonts, nss, etc.) required by a headless browser.
# Pin to a specific version for reproducibility — update intentionally.
FROM mcr.microsoft.com/playwright:v1.47.0-noble

# ── dumb-init: proper PID-1 signal forwarding & zombie reaping ───────────────
# Chrome spawns many child processes; without a proper init, SIGTERM may not
# propagate and zombie processes accumulate.
RUN apt-get update && \
    apt-get install -y --no-install-recommends dumb-init && \
    rm -rf /var/lib/apt/lists/*

# Non-root user for security hardening.
RUN groupadd --gid 10001 exporter && \
    useradd  --uid 10001 --gid exporter --shell /sbin/nologin -m exporter

# Pre-seed the Playwright-Go driver cache to bypass the broken Azure CDN.
# playwright-go v0.4700.0 looks for driver v1.47.0.
# The npm tarball naturally contains the "package/cli.js" structure required by playwright-go.
RUN apt-get update && apt-get install -y curl && \
    mkdir -p /home/exporter/.cache/ms-playwright-go/1.47.0 && \
    curl -sL https://registry.npmjs.org/playwright/-/playwright-1.47.0.tgz | tar -xz -C /home/exporter/.cache/ms-playwright-go/1.47.0 && \
    chown -R exporter:exporter /home/exporter/.cache && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy the static binary from the builder stage.
COPY --from=builder /bin/synthetic-exporter ./synthetic-exporter

# Copy default config; operators can override via a bind-mount or ConfigMap.
COPY config.yaml ./config.yaml

# Ensure the binary is executable.
RUN chmod +x ./synthetic-exporter

# Prometheus default port for exporters.
EXPOSE 9114

# Switch to the non-root user.
USER exporter

# ── Playwright browser path (pre-installed in the base image) ─────────────────
# The Playwright base image sets PLAYWRIGHT_BROWSERS_PATH so the Go library
# can locate the bundled Chromium without running `playwright install` again.
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1
ENV PLAYWRIGHT_NODEJS_PATH=/usr/bin/node

# ── dumb-init as PID 1 ───────────────────────────────────────────────────────
ENTRYPOINT ["/usr/bin/dumb-init", "--"]

# Default command — override -config and -listen-address as needed.
CMD ["./synthetic-exporter", "-config", "/app/config.yaml", "-listen-address", ":9114"]


# ── Build instructions ────────────────────────────────────────────────────────
# Build:
#   docker build -t synthetic-exporter:latest .
#
# Run (passing secrets as environment variables, NEVER in the image):
#   docker run --rm -p 9114:9114 \
#     -e APP_USERNAME="myuser" \
#     -e APP_PASSWORD="s3cr3t" \
#     -v $(pwd)/config.yaml:/app/config.yaml:ro \
#     synthetic-exporter:latest
#
# Scrape metrics:
#   curl http://localhost:9114/metrics
