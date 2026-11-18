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
	"strings"
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
	// EventCommand is run (sh -c, JSON event on stdin) when a limit is hit or a
	// chat's pool is exhausted; "" = off. The router seam (spec §5.9).
	EventCommand string `json:"eventCommand"`
	// WaitForReset: when every pool account is dry, wait for the earliest reset
	// and continue the chat (spec §5.6, opt-in).
	WaitForReset bool `json:"waitForReset"`
	// ModelFallback: on a model-specific cap, try these models on the same
	// account before switching accounts; empty = off (spec §5.5).
	ModelFallback []string `json:"modelFallback"`
	// ModelNotice is the message for a model fallback ({account} {from} {to}).
	ModelNotice string `json:"modelNotice"`
	// ResumeMessage is sent when a waiting chat resumes after a reset.
	ResumeMessage string `json:"resumeMessage"`
}

// Defaults (decided with the user 2026-11-17).
func Defaults() Config {
	return Config{
		Trigger:       "proactive",
		Threshold:     90,
		Mode:          "auto",
		Notice:        "[switcch] {from} hit its usage limit — continuing on {to}.",
		ModelFallback: []string{"sonnet"},
		ModelNotice:   "[switcch] {account} hit its {from} limit — continuing with {to}.",
		ResumeMessage: "[switcch] The usage limit has reset — continue where you left off.",
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
	// Keys absent from the file kept their defaults (we decoded onto them);
	// a present-but-invalid value is an error, never a silent behaviour change.
	d := Defaults()
	for k, v := range map[string]*string{"notice": &c.Notice, "modelNotice": &c.ModelNotice, "resumeMessage": &c.ResumeMessage} {
		if *v == "" {
			*v = map[string]string{"notice": d.Notice, "modelNotice": d.ModelNotice, "resumeMessage": d.ResumeMessage}[k]
		}
	}
	checks := [][2]string{
		{"trigger", c.Trigger},
		{"threshold", strconv.FormatFloat(c.Threshold, 'f', -1, 64)},
		{"mode", c.Mode},
		{"idleMigrateMinutes", strconv.Itoa(c.IdleMigrateMinutes)},
	}
	for _, kv := range checks {
		probe := c
		if err := Validate(&probe, kv[0], kv[1]); err != nil {
			return Defaults(), fmt.Errorf("config: %s: %w", p, err)
		}
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
	case "eventCommand":
		c.EventCommand = value
	case "waitForReset":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("waitForReset must be true or false, got %q", value)
		}
		c.WaitForReset = b
	case "modelFallback":
		models := []string{}
		for _, m := range strings.Split(value, ",") {
			if m = strings.TrimSpace(m); m != "" {
				models = append(models, m)
			}
		}
		c.ModelFallback = models
	case "modelNotice":
		c.ModelNotice = value
	case "resumeMessage":
		c.ResumeMessage = value
	case "idleMigrateMinutes":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return fmt.Errorf("idleMigrateMinutes must be 0 or more, got %q", value)
		}
		c.IdleMigrateMinutes = n
	default:
		return fmt.Errorf("unknown setting %q (trigger, threshold, mode, notice, idleMigrateMinutes, eventCommand, waitForReset, modelFallback, modelNotice, resumeMessage)", key)
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
