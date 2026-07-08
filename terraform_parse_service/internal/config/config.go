// Package config loads the service's YAML configuration and applies defaults.
package config

import (
	"fmt"
	"os"
	"time"

	uberconfig "go.uber.org/config"
)

// ProviderConfig describes where a provider's templates are loaded from, how
// often they are polled for changes, and where generated Terraform is written.
type ProviderConfig struct {
	TemplatesDir          string `yaml:"templates_dir"`
	TemplatesPollInterval string `yaml:"templates_poll_interval"`
	StorageDir            string `yaml:"storage_dir"`
}

// LoggerConfig controls zap log level and static metadata attached to every log.
type LoggerConfig struct {
	Level    string            `yaml:"level"`
	Metadata map[string]string `yaml:"metadata"`
}

// TracingConfig controls OpenTelemetry trace export and sampling.
type TracingConfig struct {
	Exporter    string  `yaml:"exporter"`
	Endpoint    string  `yaml:"endpoint"`
	Insecure    bool    `yaml:"insecure"`
	SampleRatio float64 `yaml:"sample_ratio"`
}

// MetricsConfig configures the Prometheus scrape endpoint.
type MetricsConfig struct {
	Addr string `yaml:"addr"`
}

// Config is the complete runtime configuration for the HTTP service.
type Config struct {
	ListenAddr  string                    `yaml:"listen_addr"`
	ServiceName string                    `yaml:"service_name"`
	Environment string                    `yaml:"environment"`
	Version     string                    `yaml:"version"`
	Logger      LoggerConfig              `yaml:"logger"`
	Tracing     TracingConfig             `yaml:"tracing"`
	Metrics     MetricsConfig             `yaml:"metrics"`
	Providers   map[string]ProviderConfig `yaml:"providers"`
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
	if cfg.Tracing.Exporter == "" {
		cfg.Tracing.Exporter = "stdout"
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
	if cfg.Tracing.SampleRatio < 0 || cfg.Tracing.SampleRatio > 1 {
		return fmt.Errorf("tracing.sample_ratio must be between 0 and 1")
	}
	switch cfg.Tracing.Exporter {
	case "stdout", "otlp_grpc":
	default:
		return fmt.Errorf("unknown trace exporter %q: supported values are stdout, otlp_grpc", cfg.Tracing.Exporter)
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
		if pcfg.TemplatesPollInterval != "" {
			interval, err := time.ParseDuration(pcfg.TemplatesPollInterval)
			if err != nil {
				return fmt.Errorf("providers.%s.templates_poll_interval is invalid: %w", provider, err)
			}
			if interval <= 0 {
				return fmt.Errorf("providers.%s.templates_poll_interval must be positive", provider)
			}
		}
	}
	return nil
}
