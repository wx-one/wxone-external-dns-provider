package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const defaultTenant = "wizardtales.com"

func main() {
	cfg := loadConfig()
	prov, err := newProvider(cfg)
	if err != nil {
		log.Fatal(err)
	}

	providerMux := http.NewServeMux()
	providerMux.HandleFunc("/", prov.negotiate)
	providerMux.HandleFunc("/records", prov.records)
	providerMux.HandleFunc("/adjustendpoints", prov.adjustEndpoints)

	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	providerSrv := &http.Server{
		Addr:              cfg.ProviderAddr,
		Handler:           providerMux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	healthSrv := &http.Server{
		Addr:              cfg.HealthAddr,
		Handler:           healthMux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		log.Printf("provider listening on %s", cfg.ProviderAddr)
		errCh <- providerSrv.ListenAndServe()
	}()
	go func() {
		log.Printf("health listening on %s", cfg.HealthAddr)
		errCh <- healthSrv.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Printf("received %s, shutting down", sig)
	case err = <-errCh:
		if err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = providerSrv.Shutdown(ctx)
	_ = healthSrv.Shutdown(ctx)
}

func loadConfig() config {
	var cfg config

	flag.StringVar(&cfg.Host, "host", envOr("WX1_HOST", ""), "WX ONE API base URL")
	flag.StringVar(&cfg.Username, "username", envOr("WX1_API_KEY_ID", ""), "WX ONE API key id")
	flag.StringVar(&cfg.Password, "password", envOr("WX1_API_KEY_SECRET", ""), "WX ONE API key secret")
	flag.StringVar(&cfg.Tenant, "tenant", envOr("WX1_TENANT", defaultTenant), "WX ONE tenant")
	flag.StringVar(&cfg.ProjectID, "project-id", envOr("WX1_PROJECT_ID", ""), "explicit WX ONE project id")
	flag.StringVar(&cfg.ZoneID, "zone-id", envOr("WX1_ZONE_ID", ""), "explicit WX ONE zone id")
	flag.StringVar(&cfg.ProviderAddr, "provider-addr", envOr("WX1_PROVIDER_ADDR", "127.0.0.1:8888"), "address for provider endpoints")
	flag.StringVar(&cfg.HealthAddr, "health-addr", envOr("WX1_HEALTH_ADDR", "0.0.0.0:8080"), "address for health endpoint")
	flag.StringVar(&cfg.FilterCSV, "filters", envOr("WX1_DOMAIN_FILTERS", ""), "comma-separated domain filters")
	flag.DurationVar(&cfg.AuthCacheTTL, "auth-cache-ttl", envDurationOr("WX1_AUTH_CACHE_TTL", 4*time.Hour), "how long to reuse a login cookie before refreshing it")
	flag.Parse()

	cfg.Filters = parseCSV(cfg.FilterCSV)
	return cfg
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func parseCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envDurationOr(key string, fallback time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil && d > 0 {
			return d
		}
	}
	return fallback
}

type config struct {
	Host         string
	Username     string
	Password     string
	Tenant       string
	ProjectID    string
	ZoneID       string
	AuthCacheTTL time.Duration
	Filters      []string
	FilterCSV    string
	ProviderAddr string
	HealthAddr   string
}

func newProvider(cfg config) (*provider, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("host is required")
	}
	if cfg.Username == "" || cfg.Password == "" {
		return nil, fmt.Errorf("WX1_API_KEY_ID and WX1_API_KEY_SECRET are required")
	}
	if cfg.AuthCacheTTL <= 0 {
		cfg.AuthCacheTTL = 4 * time.Hour
	}
	return &provider{cfg: cfg}, nil
}
