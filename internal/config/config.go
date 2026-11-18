// Package config holds switcch's user settings (failover trigger, threshold,
// mode, notice text), stored at ${XDG_CONFIG_HOME:-~/.config}/switcch/config.json.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Config is switcch's user configuration. Zero-value fields take defaults.
type Config struct {
	// Trigger: "proactive" (switch at Threshold, at a turn end) or "reactive"
	// (switch only on an actual limit hit).
	Trigger string `json:"trigger"`
	// Threshold: % of the 5h or 7d window (whichever is higher), 1–100.
	Threshold float64 `json:"threshold"`
	// Mode: "auto" (switch) or "advisory" (notify only).
	Mode string `json:"mode"`
	// Notice is the in-chat message shown when a chat is moved.
	Notice string `json:"notice"`
	// IdleMigrateMinutes > 0 also moves chats idle that long; 0 = off.
	IdleMigrateMinutes int `json:"idleMigrateMinutes"`
}

// Defaults (decided with the user 2026-11-17).
func Defaults() Config {
	return Config{
		Trigger:   "proactive",
		Threshold: 90,
		Mode:      "auto",
		Notice:    "[switcch] {from} hit its usage limit — continuing on {to}.",
	}
}

func path() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "switcch", "config.json"), nil
}

// Load reads the config, filling unset fields with defaults.
func Load() (Config, error) {
	c := Defaults()
	p, err := path()
	if err != nil {
		return c, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return Defaults(), fmt.Errorf("config: %s is not valid JSON: %w", p, err)
	}
	d := Defaults()
	if c.Trigger == "" {
		c.Trigger = d.Trigger
	}
	if c.Threshold == 0 {
		c.Threshold = d.Threshold
	}
	if c.Mode == "" {
		c.Mode = d.Mode
	}
	if c.Notice == "" {
		c.Notice = d.Notice
	}
	return c, nil
}

// Validate checks one key/value without persisting it.
func Validate(c *Config, key, value string) error {
	switch key {
	case "trigger":
		if value != "proactive" && value != "reactive" {
			return fmt.Errorf("trigger must be proactive or reactive, got %q", value)
		}
		c.Trigger = value
	case "threshold":
		n, err := strconv.ParseFloat(value, 64)
		if err != nil || n < 1 || n > 100 {
			return fmt.Errorf("threshold must be a number from 1 to 100, got %q", value)
		}
		c.Threshold = n
	case "mode":
		if value != "auto" && value != "advisory" {
			return fmt.Errorf("mode must be auto or advisory, got %q", value)
		}
		c.Mode = value
	case "notice":
		c.Notice = value
	case "idleMigrateMinutes":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return fmt.Errorf("idleMigrateMinutes must be 0 or more, got %q", value)
		}
		c.IdleMigrateMinutes = n
	default:
		return fmt.Errorf("unknown setting %q (trigger, threshold, mode, notice, idleMigrateMinutes)", key)
	}
	return nil
}

// Set validates and persists one setting.
func Set(key, value string) error {
	c, err := Load()
	if err != nil {
		return err
	}
	if err := Validate(&c, key, value); err != nil {
		return err
	}
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
