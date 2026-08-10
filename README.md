# 🚀 Synthetic Exporter (Go + Playwright + Prometheus)

An enterprise-grade, high-performance synthetic monitoring exporter written in **Go**. It continuously checks API HTTP statuses, TLS certificate expiration timestamps, and executes full multi-step **headless browser UI scenarios** via **Playwright for Go**, exposing rich **Prometheus metrics**.

---

## ✨ Features

- 📜 **Declarative YAML Configuration**: Define multiple synthetic UI scenarios with modular step actions (`goto`, `fill`, `click`, `wait_for_selector`, `screenshot`).
- 🔀 **Multi-Scenario Support**: Run independent UI journeys (e.g., login, search, checkout) each with their own steps, tracked separately in Prometheus with a `scenario` label.
- 🌐 **Advanced API Configuration**: Configure API endpoints as objects with a `url` and optional custom `headers` (e.g., `Authorization: Bearer ...`). Null or omitted headers are handled gracefully.
- 🏷️ **Custom Error Labelling**: Map step-specific failures to predefined `error_type_name` labels (e.g., `login_failed`, `cart_button_missing`) for instant alert categorization.
- 🔐 **Secure Secret Management**: Reference environment variables directly in configuration (`${ENV_VAR}`)—secrets are never hardcoded.
- 🎭 **Playwright for Go**: Full headless browser automation for reliable UI testing and interaction timing.
- ⚡ **Concurrent API Checks**: HTTP/SSL checks run in parallel via `sync.WaitGroup` for minimal total check duration.
- 📊 **Prometheus Integration**: Exposes HTTP status codes, SSL cert expiration timestamps (Unix epoch seconds), step duration timing, and journey error counts labeled by `service` and `scenario`.
- 🔄 **Background Worker Architecture**: Synthetic checks run in a background goroutine on a configurable interval. Prometheus scrapes instantly return cached metrics—no browser launch on scrape.
- 🐳 **Production-Ready Containerization**: Multi-stage Dockerfile leveraging official Playwright image with `dumb-init` (PID 1) for zombie Chrome process reaping and non-root execution.

---

## 📊 Exposed Prometheus Metrics

All metrics include the label `service` for seamless multi-tenant aggregation:

| Metric Name | Type | Description | Labels |
|---|---|---|---|
| `synthetic_http_status_code` | Gauge | Response HTTP status code for API endpoints (-1 on network failure). | `service`, `endpoint` |
| `synthetic_ssl_cert_expiry_timestamp_seconds` | Gauge | Unix timestamp (seconds) of the TLS certificate expiration date. | `service`, `endpoint` |
| `synthetic_ui_step_duration_seconds` | Gauge | Elapsed time in seconds for a specific UI journey step. | `service`, `scenario`, `step_name`, `action` |
| `synthetic_ui_journey_success` | Gauge | `1` if entire scenario passed, `0` if any step failed. | `service`, `scenario` |
| `synthetic_ui_journey_errors_total` | Counter | Total journey errors incremented at the exact point of failure. | `service`, `scenario`, `step_name`, `error_type_name` |

---

## 📋 Example Output

A realistic raw response from `curl http://localhost:10050/metrics`:

```
# HELP synthetic_http_status_code HTTP response status code returned by the API endpoint.
# TYPE synthetic_http_status_code gauge
synthetic_http_status_code{endpoint="https://api.github.com/zen",service="github-production"} 200
synthetic_http_status_code{endpoint="https://github.com/status",service="github-production"} 200

# HELP synthetic_ssl_cert_expiry_timestamp_seconds Unix timestamp of the TLS certificate expiration date.
# TYPE synthetic_ssl_cert_expiry_timestamp_seconds gauge
synthetic_ssl_cert_expiry_timestamp_seconds{endpoint="https://api.github.com/zen",service="github-production"} 1.7839584e+09
synthetic_ssl_cert_expiry_timestamp_seconds{endpoint="https://github.com/status",service="github-production"} 1.7839584e+09

# HELP synthetic_ui_step_duration_seconds Elapsed time in seconds for each UI journey step.
# TYPE synthetic_ui_step_duration_seconds gauge
synthetic_ui_step_duration_seconds{action="goto",scenario="login_journey",service="github-production",step_name="navigate_to_github_login"} 1.204
synthetic_ui_step_duration_seconds{action="fill",scenario="login_journey",service="github-production",step_name="fill_username"} 0.087
synthetic_ui_step_duration_seconds{action="fill",scenario="login_journey",service="github-production",step_name="fill_password"} 0.062
synthetic_ui_step_duration_seconds{action="click",scenario="login_journey",service="github-production",step_name="click_login_button"} 0.341
synthetic_ui_step_duration_seconds{action="wait_for_selector",scenario="login_journey",service="github-production",step_name="wait_for_dashboard_avatar"} 2.105
synthetic_ui_step_duration_seconds{action="goto",scenario="search_journey",service="github-production",step_name="navigate_to_github_home"} 0.952
synthetic_ui_step_duration_seconds{action="click",scenario="search_journey",service="github-production",step_name="click_search_button"} 0.124
synthetic_ui_step_duration_seconds{action="fill",scenario="search_journey",service="github-production",step_name="fill_search_query"} 0.078
synthetic_ui_step_duration_seconds{action="wait_for_selector",scenario="search_journey",service="github-production",step_name="wait_for_search_results"} 1.432

# HELP synthetic_ui_journey_success 1 if the full UI journey completed without errors, 0 otherwise.
# TYPE synthetic_ui_journey_success gauge
synthetic_ui_journey_success{scenario="login_journey",service="github-production"} 1
synthetic_ui_journey_success{scenario="search_journey",service="github-production"} 1

# HELP synthetic_ui_journey_errors_total Total number of UI journey failures, labelled by the step's error_type_name.
# TYPE synthetic_ui_journey_errors_total counter
synthetic_ui_journey_errors_total{error_type_name="navigation_failed",scenario="login_journey",service="github-production",step_name="navigate_to_github_login"} 0
```

---

## ⚙️ Configuration (`config.yaml`)

```yaml
service: "github-production"

# API endpoints — each with a url and optional headers map.
# If headers is omitted or null, the request proceeds without custom headers.
api_endpoints:
  - url: "https://api.github.com/zen"
    headers:
      Authorization: "${GITHUB_TOKEN}"
      Accept: "application/vnd.github.v3+json"
  - url: "https://github.com/status"

# UI scenarios — each scenario is an independent user journey.
# Scenarios run sequentially to avoid OOM from concurrent browsers.
scenarios:
  - name: "login_journey"
    steps:
      - name: "navigate_to_github_login"
        action: "goto"
        target: "https://github.com/login"
        error_type_name: "navigation_failed"

      - name: "fill_username"
        action: "fill"
        target: "xpath=//input[@id='login_field']"
        value: "${GITHUB_USERNAME}"
        error_type_name: "username_field_missing"

      - name: "fill_password"
        action: "fill"
        target: "xpath=//input[@id='password']"
        value: "${GITHUB_PASSWORD}"
        error_type_name: "password_field_missing"

      - name: "click_login_button"
        action: "click"
        target: "xpath=//input[@type='submit' and @name='commit']"
        error_type_name: "login_failed"

      - name: "wait_for_dashboard_avatar"
        action: "wait_for_selector"
        target: "xpath=//img[contains(@class, 'avatar')]"
        error_type_name: "dashboard_load_timeout"

  - name: "search_journey"
    steps:
      - name: "navigate_to_github_home"
        action: "goto"
        target: "https://github.com"
        error_type_name: "navigation_failed"

      - name: "click_search_button"
        action: "click"
        target: "xpath=//button[@data-target='qbsearch-input.inputButton']"
        error_type_name: "search_button_missing"

      - name: "fill_search_query"
        action: "fill"
        target: "xpath=//input[@id='query-builder-test']"
        value: "synthetic-exporter"
        error_type_name: "search_input_missing"

      - name: "wait_for_search_results"
        action: "wait_for_selector"
        target: "xpath=//div[contains(@class, 'search-results')]"
        error_type_name: "search_results_timeout"
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
export GITHUB_USERNAME="my_test_user"
export GITHUB_PASSWORD="my_secret_password"
export GITHUB_TOKEN="Bearer ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

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
  -e GITHUB_USERNAME="my_test_user" \
  -e GITHUB_PASSWORD="my_secret_password" \
  -e GITHUB_TOKEN="Bearer ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" \
  -v $(pwd)/config.yaml:/app/config.yaml:ro \
  synthetic-exporter:latest
```

---

## 🏗️ Architecture

The exporter uses a **background worker pattern** with **concurrent API checks** and **sequential UI scenarios**:

1. **Startup**: `playwright.Install()` runs once to ensure browsers are available.
2. **Background goroutine**: A `time.Ticker` (default 60s, configurable via `--check-interval`) triggers checks in an infinite loop:
   - **API checks** run concurrently via `sync.WaitGroup` — HTTP requests are lightweight I/O-bound tasks.
   - **UI scenarios** run sequentially — headless browsers are CPU/RAM-intensive. Concurrent Playwright contexts in a single container risk OOM crashes.
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
