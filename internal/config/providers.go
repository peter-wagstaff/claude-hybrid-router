package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// ModelConfig supports both simple string ("qwen3:32b") and expanded form with per-model overrides.
type ModelConfig struct {
	Model     string   `yaml:"model"`
	MaxTokens int      `yaml:"max_tokens,omitempty"`
	Transform []string `yaml:"transform,omitempty"` // per-model override (replaces provider-level)
}

// UnmarshalYAML allows ModelConfig to be a plain string or a map.
func (mc *ModelConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		mc.Model = value.Value
		return nil
	}
	type raw ModelConfig
	return value.Decode((*raw)(mc))
}

// ProviderConfig represents a single OpenAI-compatible provider.
type ProviderConfig struct {
	Name      string                  `yaml:"name"`
	Endpoint  string                  `yaml:"endpoint"`
	APIKey    string                  `yaml:"api_key"`
	MaxTokens int                     `yaml:"max_tokens,omitempty"`  // cap max_tokens for this provider
	Transform []string                `yaml:"transform,omitempty"`   // transform chain (auto-detected from name if empty)
	Models    map[string]ModelConfig  `yaml:"models"`                // label → backend model name or config
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
	MaxTokens int      // cap max_tokens (0 = no cap)
	Transform []string // transform chain
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
		providerTransform := detectTransform(p.Transform, p.Name)

		for label, mc := range p.Models {
			if _, exists := models[label]; exists {
				return nil, fmt.Errorf("duplicate model label %q", label)
			}
			// Per-model transform overrides provider-level
			transform := providerTransform
			if len(mc.Transform) > 0 {
				transform = mc.Transform
			}
			// Per-model max_tokens overrides provider-level (if set)
			maxTokens := p.MaxTokens
			if mc.MaxTokens > 0 {
				maxTokens = mc.MaxTokens
			}
			models[label] = ResolvedModel{
				Endpoint:  endpoint,
				Model:     mc.Model,
				APIKey:    apiKey,
				Label:     label,
				Provider:  p.Name,
				MaxTokens: maxTokens,
				Transform: transform,
			}
		}
	}
	return &ModelResolver{models: models}, nil
}

// detectTransform returns the transform chain to use.
// If explicit is set, use it. Otherwise auto-detect from provider name with "schema:" prefix.
func detectTransform(explicit []string, providerName string) []string {
	if len(explicit) > 0 {
		return explicit
	}
	name := strings.ToLower(providerName)
	for _, known := range []string{"openai", "gemini", "ollama"} {
		if strings.Contains(name, known) {
			return []string{"schema:" + known}
		}
	}
	return []string{"schema:generic"}
}

// Resolve looks up a model label and returns its provider details.
func (r *ModelResolver) Resolve(label string) (ResolvedModel, error) {
	m, ok := r.models[label]
	if !ok {
		return ResolvedModel{}, fmt.Errorf("unknown model label %q", label)
	}
	return m, nil
}
