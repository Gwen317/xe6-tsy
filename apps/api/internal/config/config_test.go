package config

import "testing"

func TestLoadUsesDefaults(t *testing.T) {
	t.Setenv("XE6_API_ADDRESS", "")
	t.Setenv("XE6_GIN_MODE", "")

	cfg := Load()
	if cfg.Address != "127.0.0.1:8080" {
		t.Fatalf("Address = %q, want %q", cfg.Address, "127.0.0.1:8080")
	}
	if cfg.Mode != "release" {
		t.Fatalf("Mode = %q, want %q", cfg.Mode, "release")
	}
}

func TestLoadUsesEnvironment(t *testing.T) {
	t.Setenv("XE6_API_ADDRESS", "127.0.0.1:9090")
	t.Setenv("XE6_GIN_MODE", "test")

	cfg := Load()
	if cfg.Address != "127.0.0.1:9090" {
		t.Fatalf("Address = %q, want %q", cfg.Address, "127.0.0.1:9090")
	}
	if cfg.Mode != "test" {
		t.Fatalf("Mode = %q, want %q", cfg.Mode, "test")
	}
}
