package config

import (
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Gemini GeminiConfig `yaml:"gemini"`
	Server ServerConfig `yaml:"server"`
}

type GeminiConfig struct {
	Secure1PSID     string `yaml:"GEMINI_1PSID"`
	Secure1PSIDTS   string `yaml:"GEMINI_1PSIDTS"`
	Secure1PSIDCC   string `yaml:"GEMINI_1PSIDCC"`
	RefreshInterval int    `yaml:"GEMINI_REFRESH_INTERVAL"`
}

type ServerConfig struct {
	Port string `yaml:"PORT"`
}

const (
	defaultServerPort            = "3000"
	defaultGeminiRefreshInterval = 5
)

func New() (*Config, error) {
	path := "config.yml"
	var cfg Config
	
	// Load from YAML
	data, err := os.ReadFile(path)
	if err == nil {
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	// Environment overrides
	override := func(envName string, target *string) {
		if val := os.Getenv(envName); val != "" {
			*target = val
		}
	}

	override("GEMINI_1PSID", &cfg.Gemini.Secure1PSID)
	override("GEMINI_1PSIDTS", &cfg.Gemini.Secure1PSIDTS)
	override("GEMINI_1PSIDCC", &cfg.Gemini.Secure1PSIDCC)
	override("PORT", &cfg.Server.Port)

	if refresh := os.Getenv("GEMINI_REFRESH_INTERVAL"); refresh != "" {
		if val, err := strconv.Atoi(refresh); err == nil {
			cfg.Gemini.RefreshInterval = val
		}
	}

	// Default values
	if cfg.Server.Port == "" {
		cfg.Server.Port = defaultServerPort
	}
	if cfg.Gemini.RefreshInterval <= 0 {
		cfg.Gemini.RefreshInterval = defaultGeminiRefreshInterval
	}

	return &cfg, nil
}
