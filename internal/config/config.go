// Package config is where the CLI keeps what it knows between runs: the
// installations it is logged in to, as contexts, in config.yaml - never a
// secret - and their keys' secrets, in the OS keychain.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"gopkg.in/yaml.v3"
)

// The CLI's files are its owner's alone: they name the installations and keys.
const (
	dirMode  = 0o700
	fileMode = 0o600
)

// Config is config.yaml.
type Config struct {
	Current  string              `yaml:"current,omitempty"`
	Contexts map[string]*Context `yaml:"contexts,omitempty"`

	path string
}

// Context is one installation and the key the CLI uses with it.
type Context struct {
	URL   string `yaml:"url"`
	KeyID string `yaml:"keyId"`
	// User is who the key acts as, when it was saved: for `context ls`.
	User string `yaml:"user,omitempty"`
}

// Dir is the directory the CLI keeps its files in: $HIVEPAAS_CONFIG_DIR, else
// $XDG_CONFIG_HOME/hivepaas, else ~/.config/hivepaas - on macOS too, as gh does -
// and %AppData%\hivepaas on Windows.
func Dir(getenv func(string) string) (string, error) {
	if dir := getenv("HIVEPAAS_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	if dir := getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "hivepaas"), nil
	}
	if runtime.GOOS == "windows" {
		if dir := getenv("AppData"); dir != "" {
			return filepath.Join(dir, "hivepaas"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding the home directory: %w", err)
	}
	return filepath.Join(home, ".config", "hivepaas"), nil
}

// Load reads config.yaml in dir; a missing file is an empty config.
func Load(dir string) (*Config, error) {
	cfg := &Config{Contexts: map[string]*Context{}, path: filepath.Join(dir, "config.yaml")}
	data, err := os.ReadFile(cfg.path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", cfg.path, err)
	}
	if err = yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("reading %s: %w", cfg.path, err)
	}
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]*Context{}
	}
	return cfg, nil
}

// Save writes config.yaml, readable by its owner only: it holds no secret, but
// names the installations and keys.
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), dirMode); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(c.path), err)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("writing %s: %w", c.path, err)
	}
	if err = os.WriteFile(c.path, data, fileMode); err != nil {
		return fmt.Errorf("writing %s: %w", c.path, err)
	}
	return nil
}
