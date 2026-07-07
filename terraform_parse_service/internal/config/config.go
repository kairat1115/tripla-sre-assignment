// Package config loads the service's YAML configuration and applies defaults.
package config

import (
	"fmt"
	"os"

	uberconfig "go.uber.org/config"
)

// ProviderConfig describes where a provider's Terraform templates are loaded
// from and where generated Terraform files are written.
type ProviderConfig struct {
	TemplatesDir string `yaml:"templates_dir"`
	StorageDir   string `yaml:"storage_dir"`
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
	return cfg, nil
}
