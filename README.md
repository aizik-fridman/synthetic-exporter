# Synthetic Exporter (Go + Playwright + Prometheus)

A synthetic monitoring exporter written in **Go**, designed to integrate with existing Prometheus-based monitoring stacks. It performs API HTTP status checks, TLS certificate expiration checks, and multi-step **headless browser UI scenarios** using **Playwright for Go**, exposing the results as Prometheus metrics.

The exporter focuses on running synthetic checks and exposing their results as metrics. It does not provide a monitoring UI, alerting system, dashboards, or long-term metric storage itself, and is intended to be integrated into an existing monitoring stack.

---

## Features

* **Declarative YAML Configuration**: Define multiple synthetic UI scenarios with modular step actions (`goto`, `fill`, `click`, `wait_for_selector`, `screenshot`).
* **Multi-Scenario Support**: Run independent UI journeys (e.g., login, search, checkout) each with their own steps, tracked separately in Prometheus with a `scenario` label.
* **API Configuration**: Configure API endpoints as objects with a `url` and optional custom `headers` (e.g., `Authorization: Bearer ...`). Null or omitted headers are handled gracefully.
* **Custom Error Labelling**: Map step-specific failures to predefined `error_type_name` labels (e.g., `login_failed`, `cart_button_missing`) for alert categorization in the monitoring system.
* **Environment-Based Secrets**: Reference environment variables directly in configuration (`${ENV_VAR}`) so secrets do not need to be stored in the YAML configuration.
* **Playwright for Go**: Execute headless browser automation for multi-step UI journeys.
* **Concurrent API Checks**: HTTP/TLS checks run in parallel via `sync.WaitGroup`.
* **Prometheus Metrics**: Exposes HTTP status codes, TLS certificate expiration timestamps, step durations, journey status, and journey error counters.
* **Background Worker**: Synthetic checks run in a background goroutine on a configurable interval. Prometheus scrapes return cached metrics without triggering a new browser session.
* **Docker Support**: Includes a multi-stage Dockerfile with the required Playwright runtime, `dumb-init`, and a non-root runtime user.

---

## Prometheus Metrics

The exporter exposes metrics that can be scraped by an existing Prometheus instance and then used by the surrounding monitoring stack for dashboards, alerting, recording rules, or other analysis.

All metrics include the `service` label for identifying the monitored service.

| Metric Name                                   | Type    | Description                                                          | Labels                                                |
| --------------------------------------------- | ------- | -------------------------------------------------------------------- | ----------------------------------------------------- |
| `synthetic_http_status_code`                  | Gauge   | Response HTTP status code for API endpoints (-1 on network failure). | `service`, `endpoint`                                 |
| `synthetic_ssl_cert_expiry_timestamp_seconds` | Gauge   | Unix timestamp of the TLS certificate expiration date.               | `service`, `endpoint`                                 |
| `synthetic_ui_step_duration_seconds`          | Gauge   | Elapsed time in seconds for a specific UI journey step.              | `service`, `scenario`, `step_name`, `action`          |
| `synthetic_ui_journey_success`                | Gauge   | `1` if the entire scenario passed, `0` if any step failed.           | `service`, `scenario`                                 |
| `synthetic_ui_journey_errors_total`           | Counter | Total journey errors, incremented at the point of failure.           | `service`, `scenario`, `step_name`, `error_type_name` |

---

## Grafana Dashboard

You can use the project's ready-made Grafana visualization by importing
the dashboard directly from **Grafana Labs Dashboards**.

In Grafana, go to **Dashboards → New → Import**, enter dashboard ID
**25651**, select your Prometheus data source, and import the dashboard.

![https://grafana.com/grafana/dashboards/25651-synthetic-exporter-monitoring/](grafana/screenshot.png)

## Example Output

A raw response from `curl http://localhost:10050/metrics`:

```text
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

## Configuration (`config.yaml`)

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

## Quick Start

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

Access metrics at:

```text
http://localhost:10050/metrics
```

The endpoint can then be added as a scrape target in an existing Prometheus installation.

### 2. Running with Docker

The Docker image comes with a baked-in default `config.yaml` that is configured to monitor GitHub's website. In order to use the exporter for your own environments, you must write a custom configuration file matching the targets you wish to monitor.

You can pull the pre-built image from the GitHub Container Registry (GHCR) or build it locally using the included Dockerfile.

```bash
# Option 1: Pull from GHCR
docker pull ghcr.io/aizik-fridman/synthetic-exporter:latest

# Option 2: Build locally
docker build -t synthetic-exporter:latest .

# Run container and mount your custom configuration file using a bind mount (-v)
docker run --rm -p 10050:10050 \
  -e GITHUB_USERNAME="my_test_user" \
  -e GITHUB_PASSWORD="my_secret_password" \
  -e GITHUB_TOKEN="Bearer ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" \
  -v /path/to/local/config.yaml:/app/config.yaml:ro \
  ghcr.io/aizik-fridman/synthetic-exporter:latest
```

---

## Architecture

The exporter uses a background worker pattern with concurrent API checks and sequential UI scenarios:

1. **Startup**: `playwright.Install()` runs once to ensure browsers are available.
2. **Background worker**: A `time.Ticker` (default 60s, configurable via `--check-interval`) triggers checks:

   * **API checks** run concurrently via `sync.WaitGroup`.
   * **UI scenarios** run sequentially because headless browsers are CPU/RAM-intensive.
3. **Scrape path**: When Prometheus requests `/metrics`, the exporter returns the latest cached metric values. A scrape does not start a new synthetic check or browser session.

### CLI Flags

| Flag              | Default       | Description                           |
| ----------------- | ------------- | ------------------------------------- |
| `-config`         | `config.yaml` | Path to the YAML configuration file   |
| `-listen-address` | `:10050`      | Address on which to expose `/metrics` |
| `-check-interval` | `60s`         | Interval between synthetic check runs |

---

## Docker Architecture & Process Management

The Dockerfile uses a multi-stage build:

1. **Multi-Stage Build**: Compiles a static Go binary using `golang:1.22-alpine` (`CGO_ENABLED=0`).
2. **Playwright Runtime**: Uses `mcr.microsoft.com/playwright:v1.47.0-noble` with Chromium dependencies and system libraries included.
3. **Process Management**: `dumb-init` runs as PID 1 to reap orphaned processes and forward Linux signals such as `SIGTERM` and `SIGINT`.
4. **Non-Root Execution**: The exporter runs under the `exporter` user (`uid 10001`).

---

## License

MIT License. See [LICENSE](LICENSE) for details.

```
```
