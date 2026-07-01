package config

import (
	"fmt"
	"os"

	uberconfig "go.uber.org/config"
)

type ProviderConfig struct {
	TemplatesDir string `yaml:"templates_dir"`
	StorageDir   string `yaml:"storage_dir"`
}

type LoggerConfig struct {
	Level    string            `yaml:"level"`
	Metadata map[string]string `yaml:"metadata"`
}

type TracingConfig struct {
	Exporter    string  `yaml:"exporter"`
	Endpoint    string  `yaml:"endpoint"`
	Insecure    bool    `yaml:"insecure"`
	SampleRatio float64 `yaml:"sample_ratio"`
}

type Config struct {
	ListenAddr string                    `yaml:"listen_addr"`
	Logger     LoggerConfig              `yaml:"logger"`
	Tracing    TracingConfig             `yaml:"tracing"`
	Providers  map[string]ProviderConfig `yaml:"providers"`
}

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
	return cfg, nil
}
