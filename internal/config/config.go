package config

import (
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Providers ProvidersConfig `yaml:"providers"`
	Gemini    GeminiConfig    `yaml:"gemini"`
	Claude    ClaudeConfig    `yaml:"claude"`
	OpenAI    OpenAIConfig    `yaml:"openai"`
	Server    ServerConfig    `yaml:"server"`
}

type ProvidersConfig struct {
	ProviderType string `yaml:"provider_type"`
}

type GeminiConfig struct {
	Secure1PSID     string `yaml:"GEMINI_1PSID"`
	Secure1PSIDTS   string `yaml:"GEMINI_1PSIDTS"`
	Secure1PSIDCC   string `yaml:"GEMINI_1PSIDCC"`
	RefreshInterval int    `yaml:"GEMINI_REFRESH_INTERVAL"`
	Cookies         string `yaml:"cookies"`
}

type ClaudeConfig struct {
	APIKey  string `yaml:"CLAUDE_API_KEY"`
	Model   string `yaml:"CLAUDE_MODEL"`
	Cookies string `yaml:"cookies"`
}

type OpenAIConfig struct {
	APIKey  string `yaml:"OPENAI_API_KEY"`
	Model   string `yaml:"OPENAI_MODEL"`
	Cookies string `yaml:"cookies"`
}

type ServerConfig struct {
	Port string `yaml:"PORT"`
}

const (
	defaultServerPort            = "3000"
	defaultGeminiRefreshInterval = 5
	defaultProviderType          = "gemini"
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

	override("PROVIDER_TYPE", &cfg.Providers.ProviderType)
	override("GEMINI_1PSID", &cfg.Gemini.Secure1PSID)
	override("GEMINI_1PSIDTS", &cfg.Gemini.Secure1PSIDTS)
	override("GEMINI_1PSIDCC", &cfg.Gemini.Secure1PSIDCC)
	override("GEMINI_COOKIES", &cfg.Gemini.Cookies)
	override("CLAUDE_API_KEY", &cfg.Claude.APIKey)
	override("CLAUDE_MODEL", &cfg.Claude.Model)
	override("CLAUDE_COOKIES", &cfg.Claude.Cookies)
	override("OPENAI_API_KEY", &cfg.OpenAI.APIKey)
	override("OPENAI_MODEL", &cfg.OpenAI.Model)
	override("OPENAI_COOKIES", &cfg.OpenAI.Cookies)
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
	if cfg.Providers.ProviderType == "" {
		cfg.Providers.ProviderType = defaultProviderType
	}
	if cfg.Gemini.RefreshInterval <= 0 {
		cfg.Gemini.RefreshInterval = defaultGeminiRefreshInterval
	}

	return &cfg, nil
}

