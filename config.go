package gnmireceiver

import (
	"fmt"
	"time"
)

// Represents the receiver settings within the collector's config
type Config struct {
	Target   string        `mapstructure:"target"`
	Port     int           `mapstructure:"port"`
	Paths    []string      `mapstructure:"paths"`
	Interval time.Duration `mapstructure:"interval"`
}

// Checks if receiver configuration is valid
func (cfg *Config) Validate() error {
	// Must specify target
	if cfg.Target == "" {
		return fmt.Errorf("target must be defined")
	}

	// Port must be non-negative
	if cfg.Port < 0 {
		return fmt.Errorf("invalid port, got %d", cfg.Port)
	}

	// Interval must be at least 30s
	if cfg.Interval < 30*time.Second {
		return fmt.Errorf("interval must be at least 30s, got %s", cfg.Interval)
	}

	// Must subsribe to at least one path
	if len(cfg.Paths) == 0 {
		return fmt.Errorf("must subscribe to at least one path")
	}

	return nil
}
