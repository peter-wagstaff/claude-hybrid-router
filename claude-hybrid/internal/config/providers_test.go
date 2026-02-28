package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigAndResolve(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte(`
providers:
  - name: ollama
    endpoint: http://localhost:11434/v1
    models:
      fast_coder: qwen3:32b
      reasoning: deepseek-r1:14b
  - name: together
    endpoint: https://api.together.xyz/v1/
    api_key: tok_123
    models:
      big_coder: deepseek-coder-v2-236b
`), 0644)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(cfg.Providers))
	}

	r, err := NewModelResolver(cfg)
	if err != nil {
		t.Fatalf("NewModelResolver: %v", err)
	}

	m, err := r.Resolve("fast_coder")
	if err != nil {
		t.Fatalf("Resolve fast_coder: %v", err)
	}
	if m.Endpoint != "http://localhost:11434/v1" {
		t.Errorf("unexpected endpoint: %s", m.Endpoint)
	}
	if m.Model != "qwen3:32b" {
		t.Errorf("unexpected model: %s", m.Model)
	}
	if m.Provider != "ollama" {
		t.Errorf("unexpected provider: %s", m.Provider)
	}

	m, err = r.Resolve("big_coder")
	if err != nil {
		t.Fatalf("Resolve big_coder: %v", err)
	}
	if m.APIKey != "tok_123" {
		t.Errorf("unexpected api key: %s", m.APIKey)
	}
	// Trailing slash should be trimmed
	if m.Endpoint != "https://api.together.xyz/v1" {
		t.Errorf("unexpected endpoint: %s", m.Endpoint)
	}

	_, err = r.Resolve("nonexistent")
	if err == nil {
		t.Error("expected error for unknown label")
	}
}

func TestEnvVarExpansion(t *testing.T) {
	t.Setenv("TEST_API_KEY", "secret_key_value")

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte(`
providers:
  - name: remote
    endpoint: https://api.example.com/v1
    api_key: ${TEST_API_KEY}
    models:
      test_model: gpt-4
`), 0644)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	r, err := NewModelResolver(cfg)
	if err != nil {
		t.Fatalf("NewModelResolver: %v", err)
	}

	m, _ := r.Resolve("test_model")
	if m.APIKey != "secret_key_value" {
		t.Errorf("expected expanded key, got %q", m.APIKey)
	}
}

func TestDuplicateModelLabel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte(`
providers:
  - name: a
    endpoint: http://localhost:1/v1
    models:
      dupe: model-a
  - name: b
    endpoint: http://localhost:2/v1
    models:
      dupe: model-b
`), 0644)

	cfg, _ := LoadConfig(cfgPath)
	_, err := NewModelResolver(cfg)
	if err == nil {
		t.Error("expected error for duplicate label")
	}
}

func TestMissingEndpoint(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte(`
providers:
  - name: bad
    models:
      x: y
`), 0644)

	cfg, _ := LoadConfig(cfgPath)
	_, err := NewModelResolver(cfg)
	if err == nil {
		t.Error("expected error for missing endpoint")
	}
}
