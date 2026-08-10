// Package main implements the All-in-One Synthetic Exporter.
//
// It reads a YAML config, runs HTTP/SSL checks against declared API endpoints,
// drives a headless browser via Playwright-Go through a series of UI scenarios,
// and exposes all results as Prometheus metrics on /metrics.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/playwright-community/playwright-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v3"
)

// ─── Configuration Types ─────────────────────────────────────────────────────

// Config is the top-level structure unmarshalled from config.yaml.
type Config struct {
	Service      string        `yaml:"service"`
	APIEndpoints []APIEndpoint `yaml:"api_endpoints"`
	Scenarios    []Scenario    `yaml:"scenarios"`
}

// APIEndpoint describes a single API target with an optional set of HTTP
// headers (e.g., Authorization).  If Headers is nil or empty the request
// proceeds normally without any custom headers.
type APIEndpoint struct {
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers"`
}

// Scenario groups a named sequence of UISteps into an independent user journey.
type Scenario struct {
	Name  string   `yaml:"name"`
	Steps []UIStep `yaml:"steps"`
}

// UIStep represents a single step in a synthetic UI scenario.
type UIStep struct {
	Name          string `yaml:"name"`
	Action        string `yaml:"action"` // goto | fill | click | wait_for_selector | screenshot
	Target        string `yaml:"target"` // URL (goto) or CSS/XPath selector
	Value         string `yaml:"value"`  // optional; may reference ${ENV_VAR}
	ErrorTypeName string `yaml:"error_type_name"`
}

// ─── Secret Resolution ────────────────────────────────────────────────────────

// envVarPattern matches ${VARIABLE_NAME} placeholders inside YAML values.
var envVarPattern = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// resolveSecret returns the environment variable value when the input looks
// like ${ENV_VAR}, otherwise returns the raw string untouched.
func resolveSecret(raw string) string {
	if m := envVarPattern.FindStringSubmatch(raw); len(m) == 2 {
		val := os.Getenv(m[1])
		if val == "" {
			slog.Warn("environment variable referenced in config is not set", "var", m[1])
		}
		return val
	}
	return raw
}

// ─── Prometheus Metrics ───────────────────────────────────────────────────────

// metrics holds all custom Prometheus descriptors.
type metrics struct {
	httpStatusCode         *prometheus.GaugeVec
	sslCertExpiryTimestamp *prometheus.GaugeVec
	uiStepDurationSecs    *prometheus.GaugeVec
	uiJourneySuccess      *prometheus.GaugeVec
	uiJourneyErrors       *prometheus.CounterVec
}

// newMetrics creates and registers all Prometheus metrics against the provided
// registry.  Every metric is labelled with service to allow multi-tenant
// federation.
func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		httpStatusCode: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "synthetic_http_status_code",
			Help: "HTTP response status code returned by the API endpoint.",
		}, []string{"service", "endpoint"}),

		sslCertExpiryTimestamp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "synthetic_ssl_cert_expiry_timestamp_seconds",
			Help: "Unix timestamp of the TLS certificate expiration date.",
		}, []string{"service", "endpoint"}),

		uiStepDurationSecs: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "synthetic_ui_step_duration_seconds",
			Help: "Elapsed time in seconds for each UI journey step.",
		}, []string{"service", "scenario", "step_name", "action"}),

		uiJourneySuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "synthetic_ui_journey_success",
			Help: "1 if the full UI journey completed without errors, 0 otherwise.",
		}, []string{"service", "scenario"}),

		uiJourneyErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "synthetic_ui_journey_errors_total",
			Help: "Total number of UI journey failures, labelled by the step's error_type_name.",
		}, []string{"service", "scenario", "step_name", "error_type_name"}),
	}

	reg.MustRegister(
		m.httpStatusCode,
		m.sslCertExpiryTimestamp,
		m.uiStepDurationSecs,
		m.uiJourneySuccess,
		m.uiJourneyErrors,
	)
	return m
}

// ─── HTTP / SSL Checks ────────────────────────────────────────────────────────

// checkAPIEndpoints performs an HTTP GET against every configured endpoint
// concurrently using a sync.WaitGroup.  Each goroutine records the response
// status code and TLS certificate expiry.  Custom headers from the config
// are attached when present; a nil or empty Headers map is handled gracefully.
func checkAPIEndpoints(cfg *Config, m *metrics) {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
	}
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: transport,
		// Do NOT follow redirects so we capture the first status code.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	var wg sync.WaitGroup

	for _, endpoint := range cfg.APIEndpoints {
		wg.Add(1)
		go func(ep APIEndpoint) {
			defer wg.Done()

			req, err := http.NewRequest("GET", ep.URL, nil)
			if err != nil {
				slog.Error("Failed to create HTTP request", "endpoint", ep.URL, "error", err)
				m.httpStatusCode.WithLabelValues(cfg.Service, ep.URL).Set(-1)
				m.sslCertExpiryTimestamp.WithLabelValues(cfg.Service, ep.URL).Set(-1)
				return
			}

			// Attach custom headers if provided.
			// Iterating over a nil map is a safe no-op in Go.
			for k, v := range ep.Headers {
				req.Header.Set(k, resolveSecret(v))
			}

			resp, err := client.Do(req)
			if err != nil {
				slog.Error("HTTP check failed", "endpoint", ep.URL, "error", err)
				m.httpStatusCode.WithLabelValues(cfg.Service, ep.URL).Set(-1)
				m.sslCertExpiryTimestamp.WithLabelValues(cfg.Service, ep.URL).Set(-1)
				return
			}
			defer resp.Body.Close()

			m.httpStatusCode.WithLabelValues(cfg.Service, ep.URL).Set(float64(resp.StatusCode))
			slog.Info("HTTP check", "endpoint", ep.URL, "status", resp.StatusCode)

			// TLS certificate expiry — expose as absolute Unix timestamp (seconds).
			if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
				cert := resp.TLS.PeerCertificates[0]
				expiryTimestamp := float64(cert.NotAfter.Unix())
				m.sslCertExpiryTimestamp.WithLabelValues(cfg.Service, ep.URL).Set(expiryTimestamp)
				slog.Info("SSL cert expiry", "endpoint", ep.URL, "expiry_timestamp", cert.NotAfter.Unix())
			} else {
				// Plain HTTP or no peer cert info available.
				m.sslCertExpiryTimestamp.WithLabelValues(cfg.Service, ep.URL).Set(-1)
			}
		}(endpoint)
	}

	wg.Wait()
}

// ─── UI Scenario Execution ───────────────────────────────────────────────────

// runUIScenarios launches Playwright, then executes every configured scenario
// sequentially.  Scenarios are kept sequential because headless browsers are
// CPU/RAM-intensive — running them concurrently in a single container risks
// OOM kills.  Each scenario gets a fresh browser page for isolation.
//
// NOTE: playwright.Install() is called once at startup in main().
func runUIScenarios(cfg *Config, m *metrics) {
	if len(cfg.Scenarios) == 0 {
		slog.Info("No UI scenarios configured, skipping")
		return
	}

	slog.Info("Starting UI scenarios", "service", cfg.Service, "scenarios", len(cfg.Scenarios))

	pw, err := playwright.Run()
	if err != nil {
		slog.Error("playwright.Run failed", "error", err)
		for _, sc := range cfg.Scenarios {
			m.uiJourneySuccess.WithLabelValues(cfg.Service, sc.Name).Set(0)
		}
		return
	}
	defer pw.Stop() //nolint:errcheck

	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true),
		Args: []string{
			"--no-sandbox",
			"--disable-setuid-sandbox",
			"--disable-dev-shm-usage",
		},
	})
	if err != nil {
		slog.Error("browser launch failed", "error", err)
		for _, sc := range cfg.Scenarios {
			m.uiJourneySuccess.WithLabelValues(cfg.Service, sc.Name).Set(0)
		}
		return
	}
	defer browser.Close()

	// Execute each scenario sequentially to avoid OOM from concurrent browsers.
	for _, scenario := range cfg.Scenarios {
		runSingleScenario(cfg.Service, scenario, browser, m)
	}
}

// runSingleScenario opens a fresh browser page, executes all steps in the
// given scenario, and records per-step timing and success/failure metrics.
func runSingleScenario(service string, scenario Scenario, browser playwright.Browser, m *metrics) {
	slog.Info("Running scenario", "service", service, "scenario", scenario.Name, "steps", len(scenario.Steps))

	page, err := browser.NewPage()
	if err != nil {
		slog.Error("new page failed", "scenario", scenario.Name, "error", err)
		m.uiJourneySuccess.WithLabelValues(service, scenario.Name).Set(0)
		return
	}
	defer page.Close()

	journeyFailed := false

	for i, step := range scenario.Steps {
		slog.Info("Executing step", "scenario", scenario.Name, "index", i, "name", step.Name, "action", step.Action)

		stepStart := time.Now()
		stepErr := executeStep(page, step)
		stepDuration := time.Since(stepStart).Seconds()

		m.uiStepDurationSecs.WithLabelValues(service, scenario.Name, step.Name, step.Action).Set(stepDuration)

		if stepErr != nil {
			slog.Error("Step failed",
				"scenario", scenario.Name,
				"step", step.Name,
				"action", step.Action,
				"error_type", step.ErrorTypeName,
				"error", stepErr,
			)
			m.uiJourneyErrors.WithLabelValues(
				service,
				scenario.Name,
				step.Name,
				step.ErrorTypeName,
			).Inc()
			journeyFailed = true
			// Stop executing further steps after a failure — the scenario is broken.
			break
		}

		slog.Info("Step passed", "scenario", scenario.Name, "step", step.Name, "duration_s", fmt.Sprintf("%.3f", stepDuration))
	}

	if journeyFailed {
		m.uiJourneySuccess.WithLabelValues(service, scenario.Name).Set(0)
	} else {
		m.uiJourneySuccess.WithLabelValues(service, scenario.Name).Set(1)
		slog.Info("Scenario completed successfully", "service", service, "scenario", scenario.Name)
	}
}

// executeStep dispatches a single UIStep to the appropriate Playwright call.
func executeStep(page playwright.Page, step UIStep) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = ctx // Playwright-Go manages its own timeouts; we provide context for future use.

	timeout := float64(30_000) // 30 s in milliseconds

	switch strings.ToLower(step.Action) {
	case "goto":
		if _, err := page.Goto(step.Target, playwright.PageGotoOptions{
			Timeout:   &timeout,
			WaitUntil: playwright.WaitUntilStateNetworkidle,
		}); err != nil {
			return fmt.Errorf("goto %q: %w", step.Target, err)
		}

	case "fill":
		secret := resolveSecret(step.Value)
		if err := page.Fill(step.Target, secret, playwright.PageFillOptions{
			Timeout: &timeout,
		}); err != nil {
			return fmt.Errorf("fill %q: %w", step.Target, err)
		}

	case "click":
		if err := page.Click(step.Target, playwright.PageClickOptions{
			Timeout: &timeout,
		}); err != nil {
			return fmt.Errorf("click %q: %w", step.Target, err)
		}

	case "wait_for_selector":
		if _, err := page.WaitForSelector(step.Target, playwright.PageWaitForSelectorOptions{
			Timeout: &timeout,
			State:   playwright.WaitForSelectorStateVisible,
		}); err != nil {
			return fmt.Errorf("wait_for_selector %q: %w", step.Target, err)
		}

	case "screenshot":
		path := step.Target
		if path == "" {
			path = fmt.Sprintf("/tmp/screenshot_%d.png", time.Now().UnixMilli())
		}
		if _, err := page.Screenshot(playwright.PageScreenshotOptions{Path: &path}); err != nil {
			return fmt.Errorf("screenshot to %q: %w", path, err)
		}

	default:
		return fmt.Errorf("unknown action %q for step %q", step.Action, step.Name)
	}

	return nil
}

// ─── Config Loading ───────────────────────────────────────────────────────────

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing YAML config: %w", err)
	}
	if cfg.Service == "" {
		return nil, fmt.Errorf("config must specify a non-empty service")
	}
	return &cfg, nil
}

// ─── Background Worker ───────────────────────────────────────────────────────

// runBackgroundChecks runs API and UI checks on a fixed interval in the
// background, updating Prometheus metrics in-place.  This decouples test
// execution from the /metrics scrape path, eliminating race conditions and
// scrape timeouts caused by on-demand browser launches.
func runBackgroundChecks(cfg *Config, m *metrics, interval time.Duration) {
	// Run checks immediately on startup so metrics are populated before
	// the first Prometheus scrape arrives.
	slog.Info("Running initial synthetic checks")
	checkAPIEndpoints(cfg, m)
	runUIScenarios(cfg, m)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		slog.Info("Tick: running synthetic checks", "interval", interval.String())
		checkAPIEndpoints(cfg, m)
		runUIScenarios(cfg, m)
	}
}

// ─── Main ─────────────────────────────────────────────────────────────────────

func main() {
	configPath := flag.String("config", "config.yaml", "Path to the YAML configuration file")
	listenAddr := flag.String("listen-address", ":10050", "Address on which to expose /metrics")
	checkInterval := flag.Duration("check-interval", 60*time.Second, "Interval between synthetic check runs")
	flag.Parse()

	// Structured logging (Go 1.21+)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		os.Exit(1)
	}

	slog.Info("Configuration loaded",
		"service", cfg.Service,
		"api_endpoints", len(cfg.APIEndpoints),
		"scenarios", len(cfg.Scenarios),
	)

	// Install Playwright browsers exactly once at startup.
	// Fail fast if the installation cannot complete.
	slog.Info("Installing Playwright browsers (one-time setup)")
	if err := playwright.Install(&playwright.RunOptions{Browsers: []string{"chromium"}}); err != nil {
		slog.Error("Playwright browser installation failed", "error", err)
		os.Exit(1)
	}

	// Build a fresh non-default registry so we do not expose Go runtime metrics
	// alongside synthetic metrics (keep the /metrics payload focused).
	reg := prometheus.NewRegistry()
	m := newMetrics(reg)

	// Launch background worker that runs synthetic checks on a fixed interval.
	// Prometheus scrapes will instantly return the latest cached metrics.
	go runBackgroundChecks(cfg, m, *checkInterval)

	// HTTP server
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelError),
	}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<html><head><title>Synthetic Exporter — %s</title></head>
<body><h1>Synthetic Exporter</h1>
<p>Service: <strong>%s</strong></p>
<p><a href="/metrics">Metrics</a> | <a href="/healthz">Health</a></p>
</body></html>`, cfg.Service, cfg.Service)
	})

	srv := &http.Server{
		Addr:         *listenAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	slog.Info("Synthetic exporter listening", "addr", *listenAddr, "metrics_path", "/metrics")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("Server error", "error", err)
		os.Exit(1)
	}
}
