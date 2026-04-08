package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// ManagerMode determines how quotas are applied.
type ManagerMode int

const (
	ModeOff       ManagerMode = iota // No quota applied
	ModeOnlyAuto                     // All containers get default quota
	ModeOnlyRules                    // Only matched containers get quota
	ModeAll                          // Rules first, then default for unmatched
)

// MatchRule defines a container matching rule.
type MatchRule struct {
	Name     string            `mapstructure:"name"`
	Size     string            `mapstructure:"size"`
	Level    string            `mapstructure:"level"`    // container, image, pod
	Selector map[string]string `mapstructure:"selector"` // matching criteria
}

// ManagerConfig holds the quota manager configuration.
type ManagerConfig struct {
	Runtime        string `mapstructure:"runtime"`
	Socket         string `mapstructure:"socket"`
	MountPoint     string `mapstructure:"mountPoint"`
	Size           string `mapstructure:"size"`
	ProjIDMin      uint32 `mapstructure:"projidMin"`
	ProjIDMax      uint32 `mapstructure:"projidMax"`
	AutoQuota      bool   `mapstructure:"autoQuota"`
	Namespaces     []string    `mapstructure:"namespaces"`
	MatchRules     []MatchRule `mapstructure:"matchRules"`
}

// WebConfig holds the HTTP server configuration.
type WebConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Host    string `mapstructure:"host"`
	Port    int    `mapstructure:"port"`
}

// Config is the top-level configuration.
type Config struct {
	Version    string        `mapstructure:"version"`
	PersistDir string        `mapstructure:"persistDir"`
	LogLevel   string        `mapstructure:"logLevel"`
	Manager    ManagerConfig `mapstructure:"manager"`
	Web        WebConfig     `mapstructure:"web"`
}

// Mode returns the effective operating mode based on config.
func (c *Config) Mode() ManagerMode {
	hasRules := len(c.Manager.MatchRules) > 0
	hasAuto := c.Manager.AutoQuota

	switch {
	case !hasRules && !hasAuto:
		return ModeOff
	case !hasRules && hasAuto:
		return ModeOnlyAuto
	case hasRules && !hasAuto:
		return ModeOnlyRules
	default:
		return ModeAll
	}
}

// Load reads and validates configuration from the given path.
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetEnvPrefix("QUOTA")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Defaults
	v.SetDefault("version", "v1")
	v.SetDefault("persistDir", "/var/lib/quota-agent")
	v.SetDefault("logLevel", "info")
	v.SetDefault("manager.runtime", "containerd")
	v.SetDefault("manager.socket", "/run/containerd/containerd.sock")
	v.SetDefault("manager.projidMin", 1000)
	v.SetDefault("manager.projidMax", 65534)
	v.SetDefault("manager.namespaces", []string{"default", "moby", "k8s.io"})
	v.SetDefault("web.enabled", true)
	v.SetDefault("web.host", "0.0.0.0")
	v.SetDefault("web.port", 8080)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	return cfg, cfg.validate()
}

func (c *Config) validate() error {
	if c.Manager.ProjIDMin >= c.Manager.ProjIDMax {
		return fmt.Errorf("projidMin (%d) must be less than projidMax (%d)", c.Manager.ProjIDMin, c.Manager.ProjIDMax)
	}
	if c.Manager.MountPoint == "" {
		return fmt.Errorf("manager.mountPoint is required")
	}
	if _, err := os.Stat(c.Manager.MountPoint); err != nil {
		return fmt.Errorf("mount point %s: %w", c.Manager.MountPoint, err)
	}
	if c.Manager.Socket != "" {
		if _, err := os.Stat(c.Manager.Socket); err != nil {
			return fmt.Errorf("socket %s: %w", c.Manager.Socket, err)
		}
	}
	if c.Mode() == ModeOff {
		return fmt.Errorf("no quota mode enabled: set autoQuota=true or define matchRules")
	}
	return nil
}
