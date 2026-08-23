# ─────────────────────────────────────────────────────────────────────────────
# Multi-stage Dockerfile for the All-in-One Synthetic Exporter
# ─────────────────────────────────────────────────────────────────────────────
#
# Stage 1 — Builder
#   Uses the official Go toolchain to compile a statically linked binary.
#
# Stage 2 — Runner
#   Uses ubuntu:24.04 (multi-arch: amd64 + arm64) with Chromium and all
#   system dependencies installed manually.  dumb-init is added as PID 1
#   to properly reap zombie Chrome/Chromium child processes.
# ─────────────────────────────────────────────────────────────────────────────

# ── Stage 1: Build ────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder
ARG TARGETARCH

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
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH \
    go build \
      -trimpath \
      -ldflags="-s -w -extldflags '-static'" \
      -o /bin/synthetic-exporter \
      ./main.go


# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
# ubuntu:24.04 (Noble Numbat) provides native multi-arch support (amd64+arm64).
# We install Chromium and its dependencies from Ubuntu's repos, which ship
# native arm64 binaries — unlike the Microsoft Playwright image (amd64-only).
FROM ubuntu:24.04

# Prevent interactive prompts during apt installs.
ENV DEBIAN_FRONTEND=noninteractive

# ── Install Chromium, Node.js, dumb-init, and all browser dependencies ────────
# chromium-browser from Ubuntu repos ships native arm64 binaries.
# Node.js is required by the Playwright driver.
# dumb-init provides proper PID-1 signal forwarding & zombie reaping.
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      chromium-browser \
      fonts-liberation \
      fonts-noto-color-emoji \
      fonts-noto-cjk \
      libatk-bridge2.0-0 \
      libatk1.0-0 \
      libcups2 \
      libdrm2 \
      libgbm1 \
      libnss3 \
      libxcomposite1 \
      libxdamage1 \
      libxrandr2 \
      libxkbcommon0 \
      libpango-1.0-0 \
      libcairo2 \
      libasound2t64 \
      libatspi2.0-0 \
      xdg-utils \
      nodejs \
      npm \
      dumb-init \
      libxfixes3 \
      ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Non-root user for security hardening.
RUN groupadd --gid 10001 exporter && \
    useradd  --uid 10001 --gid exporter --shell /sbin/nologin -m exporter

# Pre-seed the Playwright-Go driver cache to bypass the broken Azure CDN.
# playwright-go v0.4700.0 looks for driver v1.47.0.
# npm install fetches the driver and dependencies, and we symlink it to "package" for playwright-go.
RUN mkdir -p /home/exporter/.cache/ms-playwright-go/1.47.0 && \
    cd /home/exporter/.cache/ms-playwright-go/1.47.0 && \
    npm install playwright@1.47.0 && \
    ln -s node_modules/playwright package && \
    chown -R exporter:exporter /home/exporter/.cache

WORKDIR /app

# Copy the static binary from the builder stage.
COPY --from=builder /bin/synthetic-exporter ./synthetic-exporter

# Copy default config; operators can override via a bind-mount or ConfigMap.
COPY config.yaml ./config.yaml

# Ensure the binary is executable.
RUN chmod +x ./synthetic-exporter

# Prometheus default port for exporters.
EXPOSE 10050

# Switch to the non-root user.
USER exporter

# ── Playwright browser path ──────────────────────────────────────────────────
# Point Playwright at the system-installed Chromium so it doesn't try to
# download its own copy.
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1
ENV PLAYWRIGHT_NODEJS_PATH=/usr/bin/node
ENV PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH=/usr/bin/chromium-browser

# ── dumb-init as PID 1 ───────────────────────────────────────────────────────
ENTRYPOINT ["/usr/bin/dumb-init", "--"]

# Default command — override -config and -listen-address as needed.
CMD ["./synthetic-exporter", "-config", "/app/config.yaml", "-listen-address", ":10050"]


# ── Build instructions ────────────────────────────────────────────────────────
# Build:
#   docker build -t synthetic-exporter:latest .
#
# Run (passing secrets as environment variables, NEVER in the image):
#   docker run --rm -p 10050:10050 \
#     -e APP_USERNAME="myuser" \
#     -e APP_PASSWORD="s3cr3t" \
#     -v $(pwd)/config.yaml:/app/config.yaml:ro \
#     synthetic-exporter:latest
#
# Scrape metrics:
#   curl http://localhost:10050/metrics
