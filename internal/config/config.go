// Package config merges daemon configuration from defaults, environment
// variables (CAPD_*), and command-line flags — in that order of precedence.
package config

import (
	"os"
	"strconv"
)

const (
	DefaultHost = "127.0.0.1"
	DefaultPort = 7777
)

type Config struct {
	Host string
	Port int
}

// Load returns defaults overridden by CAPD_HOST / CAPD_PORT.
// Flag overrides are applied by the cobra layer on top of this.
func Load() Config {
	cfg := Config{Host: DefaultHost, Port: DefaultPort}
	if v := os.Getenv("CAPD_HOST"); v != "" {
		cfg.Host = v
	}
	if v := os.Getenv("CAPD_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Port = p
		}
	}
	return cfg
}
