package config

import (
	"testing"
	"time"
)

func TestEnvIntValid(t *testing.T) {
	t.Setenv("TEST_ENV_INT", "42")
	got := envInt("TEST_ENV_INT", 10)
	if got != 42 {
		t.Errorf("envInt = %d, want 42", got)
	}
}

func TestEnvIntInvalid(t *testing.T) {
	t.Setenv("TEST_ENV_INT_BAD", "abc")
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for invalid envInt value")
		}
	}()
	envInt("TEST_ENV_INT_BAD", 10)
}

func TestEnvIntZero(t *testing.T) {
	t.Setenv("TEST_ENV_INT_ZERO", "0")
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for zero envInt value")
		}
	}()
	envInt("TEST_ENV_INT_ZERO", 10)
}

func TestEnvDurationValid(t *testing.T) {
	t.Setenv("TEST_ENV_DUR", "10")
	got := envDuration("TEST_ENV_DUR", 5*time.Second)
	if got != 10*time.Second {
		t.Errorf("envDuration = %v, want 10s", got)
	}
}

func TestEnvFloatValid(t *testing.T) {
	t.Setenv("TEST_ENV_FLOAT", "2.5")
	got := envFloat("TEST_ENV_FLOAT", 1.0)
	if got != 2.5 {
		t.Errorf("envFloat = %v, want 2.5", got)
	}
}
