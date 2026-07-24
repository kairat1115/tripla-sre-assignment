// Package config loads the service's YAML configuration and applies defaults.
package config

import (
	"fmt"
	"os"
	"time"

	uberconfig "go.uber.org/config"
)

const defaultTemplatePollInterval = 5 * time.Second

// ProviderConfig describes where a provider's templates are loaded from, how
// often they are polled for changes, and where generated Terraform is written.
type ProviderConfig struct {
	// TemplatesDir is the provider template root scanned for .tmpl files.
	TemplatesDir string `yaml:"templates_dir"`
	// TemplatesPollInterval controls how often TemplatesDir is checked for
	// changes. When empty, the service uses the application default.
	TemplatesPollInterval string `yaml:"templates_poll_interval"`
	// StorageDir is the provider output root for generated Terraform files.
	StorageDir string `yaml:"storage_dir"`
}

// TemplatePollInterval parses the configured polling interval or returns the
// application default when it is omitted.
func (cfg ProviderConfig) TemplatePollInterval() (time.Duration, error) {
	if cfg.TemplatesPollInterval == "" {
		return defaultTemplatePollInterval, nil
	}
	interval, err := time.ParseDuration(cfg.TemplatesPollInterval)
	if err != nil {
		return 0, fmt.Errorf("is invalid: %w", err)
	}
	if interval <= 0 {
		return 0, fmt.Errorf("must be positive")
	}
	return interval, nil
}

// LoggerConfig controls the zap log level.
type LoggerConfig struct {
	// Level is the zap log level name. Invalid values fail logger setup.
	Level string `yaml:"level"`
}

// MetricsConfig configures the Prometheus scrape endpoint.
type MetricsConfig struct {
	// Addr is the bind address for the Prometheus /metrics server.
	Addr string `yaml:"addr"`
}

// Config is the complete runtime configuration for the HTTP service.
type Config struct {
	// ListenAddr is the bind address for the public HTTP API.
	ListenAddr string `yaml:"listen_addr"`
	// ServiceName is attached to structured logs.
	ServiceName string `yaml:"service_name"`
	// Environment identifies the deployment environment in structured logs.
	Environment string `yaml:"environment"`
	// Version identifies the running build in structured logs.
	Version string `yaml:"version"`
	// Logger configures structured logging.
	Logger LoggerConfig `yaml:"logger"`
	// Metrics configures the Prometheus metrics endpoint.
	Metrics MetricsConfig `yaml:"metrics"`
	// Providers maps provider keys, such as aws, to provider runtime settings.
	Providers map[string]ProviderConfig `yaml:"providers"`
}

// Load reads the YAML configuration from CONFIG_PATH or configs/config.yaml.
// Environment variables referenced in the YAML are expanded before decoding.
func Load() (Config, error) {
	path := os.Getenv("CONFIG_PATH")
	if path == "" {
		path = "configs/config.yaml"
	}
	provider, err := uberconfig.NewYAML(
		uberconfig.File(path),
		uberconfig.Expand(os.LookupEnv),
	)
	if err != nil {
		return Config{}, fmt.Errorf("load config %s: %w", path, err)
	}
	var cfg Config
	if err := provider.Get(uberconfig.Root).Populate(&cfg); err != nil {
		return Config{}, fmt.Errorf("populate config: %w", err)
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = "terraform-parse-service"
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8080"
	}
	if cfg.Logger.Level == "" {
		cfg.Logger.Level = "info"
	}
	if cfg.Metrics.Addr == "" {
		cfg.Metrics.Addr = ":9091"
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks required fields and supported option values after defaults are
// applied.
func (cfg Config) Validate() error {
	if cfg.ListenAddr == "" {
		return fmt.Errorf("listen_addr is required")
	}
	if len(cfg.Providers) == 0 {
		return fmt.Errorf("at least one provider is required")
	}
	for provider, pcfg := range cfg.Providers {
		if pcfg.TemplatesDir == "" {
			return fmt.Errorf("providers.%s.templates_dir is required", provider)
		}
		if pcfg.StorageDir == "" {
			return fmt.Errorf("providers.%s.storage_dir is required", provider)
		}
		if _, err := pcfg.TemplatePollInterval(); err != nil {
			return fmt.Errorf("providers.%s.templates_poll_interval %w", provider, err)
		}
	}
	return nil
}
