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
	// TemplatesDir is the provider template root scanned for .tmpl files.
	TemplatesDir string `yaml:"templates_dir"`
	// TemplatesPollInterval controls how often TemplatesDir is checked for
	// changes. When empty, the service uses the application default.
	TemplatesPollInterval string `yaml:"templates_poll_interval"`
	// StorageDir is the provider output root for generated Terraform files.
	StorageDir string `yaml:"storage_dir"`
}

// LoggerConfig controls zap log level and static metadata attached to every log.
type LoggerConfig struct {
	// Level is the zap log level name. Invalid values fall back to info.
	Level string `yaml:"level"`
	// Metadata contains extra static fields attached to every service log.
	Metadata map[string]string `yaml:"metadata"`
}

// TracingConfig controls OpenTelemetry trace export and sampling.
type TracingConfig struct {
	// Exporter selects the trace exporter. Supported values are stdout and
	// otlp_grpc.
	Exporter string `yaml:"exporter"`
	// Endpoint is the OTLP/gRPC collector endpoint used by the otlp_grpc exporter.
	Endpoint string `yaml:"endpoint"`
	// Insecure disables TLS for the OTLP/gRPC exporter.
	Insecure bool `yaml:"insecure"`
	// SampleRatio is the trace sampling ratio, from 0.0 to 1.0.
	SampleRatio float64 `yaml:"sample_ratio"`
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
	// ServiceName is attached to logs, traces, and runtime metadata.
	ServiceName string `yaml:"service_name"`
	// Environment identifies the deployment environment, such as dev or prod.
	Environment string `yaml:"environment"`
	// Version identifies the running build, usually a release version or git SHA.
	Version string `yaml:"version"`
	// Logger configures structured logging.
	Logger LoggerConfig `yaml:"logger"`
	// Tracing configures OpenTelemetry tracing.
	Tracing TracingConfig `yaml:"tracing"`
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
