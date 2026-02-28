package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// loadTestConfig writes yaml to a temp file, loads and resolves it.
func loadTestConfig(t *testing.T, yaml string) (*ProvidersConfig, *ModelResolver) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	r, err := NewModelResolver(cfg)
	if err != nil {
		t.Fatalf("NewModelResolver: %v", err)
	}
	return cfg, r
}

func TestLoadConfigAndResolve(t *testing.T) {
	cfg, r := loadTestConfig(t, `
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
`)

	if len(cfg.Providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(cfg.Providers))
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

	// Auto-detect transform from provider name
	if !reflect.DeepEqual(m.Transform, []string{"schema:generic"}) {
		t.Errorf("expected [schema:generic] transform for 'together', got %v", m.Transform)
	}
	ollamaModel, _ := r.Resolve("fast_coder")
	if !reflect.DeepEqual(ollamaModel.Transform, []string{"schema:ollama"}) {
		t.Errorf("expected [schema:ollama] transform, got %v", ollamaModel.Transform)
	}
}

func TestExplicitTransform(t *testing.T) {
	_, r := loadTestConfig(t, `
providers:
  - name: my-google-provider
    endpoint: https://generativelanguage.googleapis.com/v1beta/openai
    transform: ["gemini"]
    models:
      flash: gemini-2.0-flash
`)

	m, _ := r.Resolve("flash")
	if !reflect.DeepEqual(m.Transform, []string{"gemini"}) {
		t.Errorf("expected [gemini] transform, got %v", m.Transform)
	}
}

func TestEnvVarExpansion(t *testing.T) {
	t.Setenv("TEST_API_KEY", "secret_key_value")

	_, r := loadTestConfig(t, `
providers:
  - name: remote
    endpoint: https://api.example.com/v1
    api_key: ${TEST_API_KEY}
    models:
      test_model: gpt-4
`)

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

func TestTransformArray(t *testing.T) {
	_, r := loadTestConfig(t, `
providers:
  - name: local
    endpoint: http://localhost:11434/v1
    transform: ["reasoning", "enhancetool"]
    models:
      smart: qwen3:32b
`)

	m, _ := r.Resolve("smart")
	want := []string{"reasoning", "enhancetool"}
	if !reflect.DeepEqual(m.Transform, want) {
		t.Errorf("expected %v, got %v", want, m.Transform)
	}
}

func TestTransformPerModel(t *testing.T) {
	_, r := loadTestConfig(t, `
providers:
  - name: local
    endpoint: http://localhost:11434/v1
    transform: ["reasoning"]
    models:
      default_model: qwen3:32b
      tool_model:
        model: qwen3:32b
        transform: ["tooluse", "enhancetool"]
`)

	// default_model should inherit provider-level transform
	dm, _ := r.Resolve("default_model")
	if !reflect.DeepEqual(dm.Transform, []string{"reasoning"}) {
		t.Errorf("expected provider-level [reasoning], got %v", dm.Transform)
	}

	// tool_model should use per-model override
	tm, _ := r.Resolve("tool_model")
	want := []string{"tooluse", "enhancetool"}
	if !reflect.DeepEqual(tm.Transform, want) {
		t.Errorf("expected per-model %v, got %v", want, tm.Transform)
	}
}

func TestTransformAutoDetect(t *testing.T) {
	_, r := loadTestConfig(t, `
providers:
  - name: ollama
    endpoint: http://localhost:11434/v1
    models:
      local: qwen3:32b
  - name: some-provider
    endpoint: http://localhost:8080/v1
    models:
      remote: some-model
`)

	m, _ := r.Resolve("local")
	if !reflect.DeepEqual(m.Transform, []string{"schema:ollama"}) {
		t.Errorf("expected [schema:ollama], got %v", m.Transform)
	}

	m, _ = r.Resolve("remote")
	if !reflect.DeepEqual(m.Transform, []string{"schema:generic"}) {
		t.Errorf("expected [schema:generic], got %v", m.Transform)
	}
}

func TestModelConfigMaxTokens(t *testing.T) {
	_, r := loadTestConfig(t, `
providers:
  - name: local
    endpoint: http://localhost:11434/v1
    max_tokens: 4096
    models:
      default_cap: qwen3:32b
      custom_cap:
        model: qwen3:32b
        max_tokens: 8192
`)

	dm, _ := r.Resolve("default_cap")
	if dm.MaxTokens != 4096 {
		t.Errorf("expected provider-level 4096, got %d", dm.MaxTokens)
	}

	cm, _ := r.Resolve("custom_cap")
	if cm.MaxTokens != 8192 {
		t.Errorf("expected per-model 8192, got %d", cm.MaxTokens)
	}
}
