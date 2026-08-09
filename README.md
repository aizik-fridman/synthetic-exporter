# 🚀 Synthetic Exporter (Go + Playwright + Prometheus)

An enterprise-grade, high-performance synthetic monitoring exporter written in **Go**. It continuously checks API HTTP statuses, TLS certificate expiration timestamps, and executes full multi-step **headless browser UI user journeys** via **Playwright for Go**, exposing rich **Prometheus metrics**.

---

## ✨ Features

- 📜 **Declarative YAML Configuration**: Define synthetic user journeys with modular actions (`goto`, `fill`, `click`, `wait_for_selector`, `screenshot`).
- 🏷️ **Custom Error Labelling**: Map stage-specific failures to predefined `error_type_name` labels (e.g., `login_failed`, `cart_button_missing`) for instant alert categorization.
- 🔐 **Secure Secret Management**: Reference environment variables directly in configuration (`${ENV_VAR}`)—secrets are never hardcoded.
- 🎭 **Playwright for Go**: Full headless browser automation for reliable UI testing and interaction timing.
- 📊 **Prometheus Integration**: Exposes HTTP status codes, SSL cert expiration timestamps (Unix epoch seconds), stage duration timing, and journey error counts labeled by `system_name`.
- 🔄 **Background Worker Architecture**: Synthetic checks run in a background goroutine on a configurable interval. Prometheus scrapes instantly return cached metrics—no browser launch on scrape.
- 🐳 **Production-Ready Containerization**: Multi-stage Dockerfile leveraging official Playwright image with `dumb-init` (PID 1) for zombie Chrome process reaping and non-root execution.

---

## 📊 Exposed Prometheus Metrics

All metrics include the label `system_name` for seamless multi-tenant aggregation:

| Metric Name | Type | Description | Labels |
|---|---|---|---|
| `synthetic_http_status_code` | Gauge | Response HTTP status code for API endpoints (-1 on network failure). | `system_name`, `endpoint` |
| `synthetic_ssl_cert_expiry_timestamp_seconds` | Gauge | Unix timestamp (seconds) of the TLS certificate expiration date. | `system_name`, `endpoint` |
| `synthetic_ui_stage_duration_seconds` | Gauge | Elapsed time in seconds for a specific UI journey stage. | `system_name`, `stage_name`, `action` |
| `synthetic_ui_journey_success` | Gauge | `1` if entire journey passed, `0` if any stage failed. | `system_name` |
| `synthetic_ui_journey_errors_total` | Counter | Total journey errors incremented at the exact point of failure. | `system_name`, `stage_name`, `error_type_name` |

---

## 📋 Example Output

A realistic raw response from `curl http://localhost:10050/metrics`:

```
# HELP synthetic_http_status_code HTTP response status code returned by the API endpoint.
# TYPE synthetic_http_status_code gauge
synthetic_http_status_code{endpoint="https://my-e-commerce-app.example.com/api/health",system_name="my-e-commerce-app"} 200
synthetic_http_status_code{endpoint="https://my-e-commerce-app.example.com/api/v1/products",system_name="my-e-commerce-app"} 200

# HELP synthetic_ssl_cert_expiry_timestamp_seconds Unix timestamp of the TLS certificate expiration date.
# TYPE synthetic_ssl_cert_expiry_timestamp_seconds gauge
synthetic_ssl_cert_expiry_timestamp_seconds{endpoint="https://my-e-commerce-app.example.com/api/health",system_name="my-e-commerce-app"} 1.7839584e+09
synthetic_ssl_cert_expiry_timestamp_seconds{endpoint="https://my-e-commerce-app.example.com/api/v1/products",system_name="my-e-commerce-app"} 1.7839584e+09

# HELP synthetic_ui_stage_duration_seconds Elapsed time in seconds for each UI journey stage.
# TYPE synthetic_ui_stage_duration_seconds gauge
synthetic_ui_stage_duration_seconds{action="goto",stage_name="navigate_to_login",system_name="my-e-commerce-app"} 1.204
synthetic_ui_stage_duration_seconds{action="fill",stage_name="fill_username",system_name="my-e-commerce-app"} 0.087
synthetic_ui_stage_duration_seconds{action="fill",stage_name="fill_password",system_name="my-e-commerce-app"} 0.062
synthetic_ui_stage_duration_seconds{action="click",stage_name="click_login_button",system_name="my-e-commerce-app"} 0.341
synthetic_ui_stage_duration_seconds{action="wait_for_selector",stage_name="wait_for_dashboard",system_name="my-e-commerce-app"} 2.105

# HELP synthetic_ui_journey_success 1 if the full UI journey completed without errors, 0 otherwise.
# TYPE synthetic_ui_journey_success gauge
synthetic_ui_journey_success{system_name="my-e-commerce-app"} 1

# HELP synthetic_ui_journey_errors_total Total number of UI journey failures, labelled by the stage's error_type_name.
# TYPE synthetic_ui_journey_errors_total counter
synthetic_ui_journey_errors_total{error_type_name="navigation_failed",stage_name="navigate_to_login",system_name="my-e-commerce-app"} 0
```

---

## ⚙️ Configuration (`config.yaml`)

```yaml
system_name: "my-e-commerce-app"

api_endpoints:
  - "https://my-e-commerce-app.example.com/api/health"
  - "https://my-e-commerce-app.example.com/api/v1/products"

stages:
  - name: "navigate_to_login"
    action: "goto"
    target: "https://my-e-commerce-app.example.com/login"
    error_type_name: "navigation_failed"

  - name: "fill_username"
    action: "fill"
    target: "xpath=//input[@id='username']"
    value: "${APP_USERNAME}" # Resolved from environment variable at runtime
    error_type_name: "username_field_missing"

  - name: "fill_password"
    action: "fill"
    target: "xpath=//input[@id='password']"
    value: "${APP_PASSWORD}"
    error_type_name: "password_field_missing"

  - name: "click_login_button"
    action: "click"
    target: "xpath=//button[@data-testid='login-submit']"
    error_type_name: "login_failed"

  - name: "wait_for_dashboard"
    action: "wait_for_selector"
    target: "xpath=//div[@data-testid='dashboard-container']"
    error_type_name: "dashboard_load_timeout"
```

---

## 🚀 Quick Start

### 1. Running Locally

Ensure you have **Go 1.22+** installed:

```bash
# Clone repository
git clone https://github.com/aizik-fridman/synthetic-exporter.git
cd synthetic-exporter

# Set credentials in host environment
export APP_USERNAME="my_test_user"
export APP_PASSWORD="my_secret_password"

# Download dependencies & run
go run main.go -config config.yaml -listen-address :10050
```

Access metrics at: `http://localhost:10050/metrics`

---

### 2. Running with Docker

Build and run using the optimized multi-stage `Dockerfile`:

```bash
# Build image
docker build -t synthetic-exporter:latest .

# Run container with environment variables
docker run --rm -p 10050:10050 \
  -e APP_USERNAME="my_test_user" \
  -e APP_PASSWORD="my_secret_password" \
  -v $(pwd)/config.yaml:/app/config.yaml:ro \
  synthetic-exporter:latest
```

---

## 🏗️ Architecture

The exporter uses a **background worker pattern**:

1. **Startup**: `playwright.Install()` runs once to ensure browsers are available.
2. **Background goroutine**: A `time.Ticker` (default 60s, configurable via `--check-interval`) triggers `checkAPIEndpoints` and `runUIJourney` in an infinite loop.
3. **Scrape path**: When Prometheus hits `/metrics`, it instantly returns the latest cached metric values — no browser launch, no race conditions, no timeouts.

### CLI Flags

| Flag | Default | Description |
|---|---|---|
| `-config` | `config.yaml` | Path to the YAML configuration file |
| `-listen-address` | `:10050` | Address on which to expose `/metrics` |
| `-check-interval` | `60s` | Interval between synthetic check runs |

---

## 🐳 Docker Architecture & Process Management

The `Dockerfile` follows enterprise best practices:
1. **Multi-Stage Build**: Compiles a static Go binary using `golang:1.22-alpine` (`CGO_ENABLED=0`).
2. **Pre-packaged Runtime**: Uses `mcr.microsoft.com/playwright:v1.47.0-noble` so Chromium dependencies and system libraries are built-in.
3. **Zombie Process Reaping (`dumb-init`)**: Headless Chrome creates subprocesses on every run. `dumb-init` runs as `ENTRYPOINT` (PID 1) to harvest orphan processes and forward Linux signals (`SIGTERM`, `SIGINT`) cleanly.
4. **Least Privilege**: Executes under non-root user `exporter` (`uid 10001`).

---

## 📄 License

MIT License. See [LICENSE](LICENSE) for details.
