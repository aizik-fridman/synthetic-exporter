// Package main implements the All-in-One Synthetic Exporter.
//
// It reads a YAML config, runs HTTP/SSL checks against declared API endpoints,
// drives a headless browser via Playwright-Go through a series of UI stages,
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
	"time"

	"github.com/playwright-community/playwright-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v3"
)

// ─── Configuration Types ─────────────────────────────────────────────────────

// Config is the top-level structure unmarshalled from config.yaml.
type Config struct {
	SystemName   string    `yaml:"system_name"`
	APIEndpoints []string  `yaml:"api_endpoints"`
	Stages       []UIStage `yaml:"stages"`
}

// UIStage represents a single step in the synthetic UI journey.
type UIStage struct {
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
	uiStageDurationSecs   *prometheus.GaugeVec
	uiJourneySuccess      *prometheus.GaugeVec
	uiJourneyErrors       *prometheus.CounterVec
}

// newMetrics creates and registers all Prometheus metrics against the provided
// registry.  Every metric is labelled with system_name to allow multi-tenant
// federation.
func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		httpStatusCode: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "synthetic_http_status_code",
			Help: "HTTP response status code returned by the API endpoint.",
		}, []string{"system_name", "endpoint"}),

		sslCertExpiryTimestamp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "synthetic_ssl_cert_expiry_timestamp_seconds",
			Help: "Unix timestamp of the TLS certificate expiration date.",
		}, []string{"system_name", "endpoint"}),

		uiStageDurationSecs: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "synthetic_ui_stage_duration_seconds",
			Help: "Elapsed time in seconds for each UI journey stage.",
		}, []string{"system_name", "stage_name", "action"}),

		uiJourneySuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "synthetic_ui_journey_success",
			Help: "1 if the full UI journey completed without errors, 0 otherwise.",
		}, []string{"system_name"}),

		uiJourneyErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "synthetic_ui_journey_errors_total",
			Help: "Total number of UI journey failures, labelled by the stage's error_type_name.",
		}, []string{"system_name", "stage_name", "error_type_name"}),
	}

	reg.MustRegister(
		m.httpStatusCode,
		m.sslCertExpiryTimestamp,
		m.uiStageDurationSecs,
		m.uiJourneySuccess,
		m.uiJourneyErrors,
	)
	return m
}

// ─── HTTP / SSL Checks ────────────────────────────────────────────────────────

// checkAPIEndpoints performs an HTTP GET against every configured endpoint and
// records the response status code and TLS certificate expiry.
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

	for _, endpoint := range cfg.APIEndpoints {
		ep := endpoint // capture

		resp, err := client.Get(ep)
		if err != nil {
			slog.Error("HTTP check failed", "endpoint", ep, "error", err)
			m.httpStatusCode.WithLabelValues(cfg.SystemName, ep).Set(-1)
			m.sslCertExpiryTimestamp.WithLabelValues(cfg.SystemName, ep).Set(-1)
			continue
		}
		defer resp.Body.Close()

		m.httpStatusCode.WithLabelValues(cfg.SystemName, ep).Set(float64(resp.StatusCode))
		slog.Info("HTTP check", "endpoint", ep, "status", resp.StatusCode)

		// TLS certificate expiry — expose as absolute Unix timestamp (seconds).
		if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
			cert := resp.TLS.PeerCertificates[0]
			expiryTimestamp := float64(cert.NotAfter.Unix())
			m.sslCertExpiryTimestamp.WithLabelValues(cfg.SystemName, ep).Set(expiryTimestamp)
			slog.Info("SSL cert expiry", "endpoint", ep, "expiry_timestamp", cert.NotAfter.Unix())
		} else {
			// Plain HTTP or no peer cert info available.
			m.sslCertExpiryTimestamp.WithLabelValues(cfg.SystemName, ep).Set(-1)
		}
	}
}

// ─── UI Journey Execution ─────────────────────────────────────────────────────

// runUIJourney launches Playwright, navigates through all configured stages,
// records per-stage timing, and updates the success/error metrics.
//
// NOTE: playwright.Install() is called once at startup in main().
// This function only starts the Playwright server and launches a browser.
func runUIJourney(cfg *Config, m *metrics) {
	slog.Info("Starting UI journey", "system", cfg.SystemName, "stages", len(cfg.Stages))

	pw, err := playwright.Run()
	if err != nil {
		slog.Error("playwright.Run failed", "error", err)
		m.uiJourneySuccess.WithLabelValues(cfg.SystemName).Set(0)
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
		m.uiJourneySuccess.WithLabelValues(cfg.SystemName).Set(0)
		return
	}
	defer browser.Close()

	page, err := browser.NewPage()
	if err != nil {
		slog.Error("new page failed", "error", err)
		m.uiJourneySuccess.WithLabelValues(cfg.SystemName).Set(0)
		return
	}
	defer page.Close()

	// ── Execute each stage ────────────────────────────────────────────────────
	journeyFailed := false

	for i, stage := range cfg.Stages {
		slog.Info("Executing stage", "index", i, "name", stage.Name, "action", stage.Action)

		stageStart := time.Now()
		stageErr := executeStage(page, stage)
		stageDuration := time.Since(stageStart).Seconds()

		m.uiStageDurationSecs.WithLabelValues(cfg.SystemName, stage.Name, stage.Action).Set(stageDuration)

		if stageErr != nil {
			slog.Error("Stage failed",
				"stage", stage.Name,
				"action", stage.Action,
				"error_type", stage.ErrorTypeName,
				"error", stageErr,
			)
			m.uiJourneyErrors.WithLabelValues(
				cfg.SystemName,
				stage.Name,
				stage.ErrorTypeName,
			).Inc()
			journeyFailed = true
			// Stop executing further stages after a failure — the journey is broken.
			break
		}

		slog.Info("Stage passed", "stage", stage.Name, "duration_s", fmt.Sprintf("%.3f", stageDuration))
	}

	if journeyFailed {
		m.uiJourneySuccess.WithLabelValues(cfg.SystemName).Set(0)
	} else {
		m.uiJourneySuccess.WithLabelValues(cfg.SystemName).Set(1)
		slog.Info("UI journey completed successfully", "system", cfg.SystemName)
	}
}

// executeStage dispatches a single UIStage to the appropriate Playwright call.
func executeStage(page playwright.Page, stage UIStage) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = ctx // Playwright-Go manages its own timeouts; we provide context for future use.

	timeout := float64(30_000) // 30 s in milliseconds

	switch strings.ToLower(stage.Action) {
	case "goto":
		if _, err := page.Goto(stage.Target, playwright.PageGotoOptions{
			Timeout:   &timeout,
			WaitUntil: playwright.WaitUntilStateNetworkidle,
		}); err != nil {
			return fmt.Errorf("goto %q: %w", stage.Target, err)
		}

	case "fill":
		secret := resolveSecret(stage.Value)
		if err := page.Fill(stage.Target, secret, playwright.PageFillOptions{
			Timeout: &timeout,
		}); err != nil {
			return fmt.Errorf("fill %q: %w", stage.Target, err)
		}

	case "click":
		if err := page.Click(stage.Target, playwright.PageClickOptions{
			Timeout: &timeout,
		}); err != nil {
			return fmt.Errorf("click %q: %w", stage.Target, err)
		}

	case "wait_for_selector":
		if _, err := page.WaitForSelector(stage.Target, playwright.PageWaitForSelectorOptions{
			Timeout: &timeout,
			State:   playwright.WaitForSelectorStateVisible,
		}); err != nil {
			return fmt.Errorf("wait_for_selector %q: %w", stage.Target, err)
		}

	case "screenshot":
		path := stage.Target
		if path == "" {
			path = fmt.Sprintf("/tmp/screenshot_%d.png", time.Now().UnixMilli())
		}
		if _, err := page.Screenshot(playwright.PageScreenshotOptions{Path: &path}); err != nil {
			return fmt.Errorf("screenshot to %q: %w", path, err)
		}

	default:
		return fmt.Errorf("unknown action %q for stage %q", stage.Action, stage.Name)
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
	if cfg.SystemName == "" {
		return nil, fmt.Errorf("config must specify a non-empty system_name")
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
	runUIJourney(cfg, m)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		slog.Info("Tick: running synthetic checks", "interval", interval.String())
		checkAPIEndpoints(cfg, m)
		runUIJourney(cfg, m)
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
		"system_name", cfg.SystemName,
		"api_endpoints", len(cfg.APIEndpoints),
		"stages", len(cfg.Stages),
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
<p>System: <strong>%s</strong></p>
<p><a href="/metrics">Metrics</a> | <a href="/healthz">Health</a></p>
</body></html>`, cfg.SystemName, cfg.SystemName)
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
