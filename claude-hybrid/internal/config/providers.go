package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProviderConfig represents a single OpenAI-compatible provider.
type ProviderConfig struct {
	Name      string            `yaml:"name"`
	Endpoint  string            `yaml:"endpoint"`
	APIKey    string            `yaml:"api_key"`
	MaxTokens int               `yaml:"max_tokens,omitempty"` // cap max_tokens for this provider
	Models    map[string]string `yaml:"models"`               // label → backend model name
}

// ProvidersConfig is the top-level config file structure.
type ProvidersConfig struct {
	Providers []ProviderConfig `yaml:"providers"`
}

// ResolvedModel holds the result of resolving a model label.
type ResolvedModel struct {
	Endpoint  string // e.g. "http://localhost:11434/v1"
	Model     string // backend model name, e.g. "qwen3:32b"
	APIKey    string // resolved API key (empty if none)
	Label     string // original label, e.g. "fast_coder"
	Provider  string // provider name, e.g. "ollama"
	MaxTokens int    // cap max_tokens (0 = no cap)
}

// ModelResolver resolves model labels to provider details.
type ModelResolver struct {
	models map[string]ResolvedModel
}

var envVarRE = regexp.MustCompile(`\$\{([^}]+)\}`)

// expandEnvVars replaces ${VAR} references with environment variable values.
func expandEnvVars(s string) string {
	return envVarRE.ReplaceAllStringFunc(s, func(match string) string {
		varName := envVarRE.FindStringSubmatch(match)[1]
		return os.Getenv(varName)
	})
}

// LoadConfig reads and parses a config file.
func LoadConfig(path string) (*ProvidersConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg ProvidersConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}

// NewModelResolver builds a resolver from config.
func NewModelResolver(cfg *ProvidersConfig) (*ModelResolver, error) {
	models := make(map[string]ResolvedModel)
	for _, p := range cfg.Providers {
		if p.Name == "" {
			return nil, fmt.Errorf("provider missing name")
		}
		endpoint := strings.TrimRight(p.Endpoint, "/")
		if endpoint == "" {
			return nil, fmt.Errorf("provider %q missing endpoint", p.Name)
		}
		apiKey := expandEnvVars(p.APIKey)

		for label, backendModel := range p.Models {
			if _, exists := models[label]; exists {
				return nil, fmt.Errorf("duplicate model label %q", label)
			}
			models[label] = ResolvedModel{
				Endpoint:  endpoint,
				Model:     backendModel,
				APIKey:    apiKey,
				Label:     label,
				Provider:  p.Name,
				MaxTokens: p.MaxTokens,
			}
		}
	}
	return &ModelResolver{models: models}, nil
}

// Resolve looks up a model label and returns its provider details.
func (r *ModelResolver) Resolve(label string) (ResolvedModel, error) {
	m, ok := r.models[label]
	if !ok {
		return ResolvedModel{}, fmt.Errorf("unknown model label %q", label)
	}
	return m, nil
}
