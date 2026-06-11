package config

import "testing"

func TestDefaults(t *testing.T) {
	cfg := Load()
	if cfg.Host != "127.0.0.1" || cfg.Port != 7777 || len(cfg.Origins) != 0 {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("CAPD_HOST", "0.0.0.0")
	t.Setenv("CAPD_PORT", "8123")
	t.Setenv("CAPD_ORIGINS", "app.example.com, *.corp.internal ,")
	cfg := Load()
	if cfg.Host != "0.0.0.0" || cfg.Port != 8123 {
		t.Fatalf("cfg = %+v", cfg)
	}
	if len(cfg.Origins) != 2 || cfg.Origins[0] != "app.example.com" || cfg.Origins[1] != "*.corp.internal" {
		t.Fatalf("origins = %v", cfg.Origins)
	}
}

func TestBadPortIgnored(t *testing.T) {
	t.Setenv("CAPD_PORT", "not-a-number")
	if cfg := Load(); cfg.Port != 7777 {
		t.Fatalf("port = %d", cfg.Port)
	}
}
