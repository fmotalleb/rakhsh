package config

import (
	"os"

	"go.yaml.in/yaml/v3"
)

func Write(cfg *Config, path string) error {
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}
