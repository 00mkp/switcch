package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsWhenAbsent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Trigger != "proactive" || c.Threshold != 90 || c.Mode != "auto" || c.IdleMigrateMinutes != 0 {
		t.Fatalf("defaults = %+v", c)
	}
	if c.Notice == "" {
		t.Fatal("notice default must be non-empty")
	}
}

func TestSetPersistsAndValidates(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Set("threshold", "85"); err != nil {
		t.Fatal(err)
	}
	if err := Set("trigger", "reactive"); err != nil {
		t.Fatal(err)
	}
	c, _ := Load()
	if c.Threshold != 85 || c.Trigger != "reactive" {
		t.Fatalf("after Set: %+v", c)
	}
	for _, bad := range [][2]string{
		{"threshold", "150"}, {"threshold", "0"}, {"threshold", "abc"},
		{"trigger", "sometimes"}, {"mode", "loud"}, {"nosuchkey", "x"},
	} {
		if err := Set(bad[0], bad[1]); err == nil {
			t.Fatalf("Set(%q,%q) should error", bad[0], bad[1])
		}
	}
}

func TestMalformedErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	_ = os.MkdirAll(filepath.Join(dir, "switcch"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "switcch", "config.json"), []byte("{not json"), 0o600)
	if _, err := Load(); err == nil {
		t.Fatal("malformed config must error")
	}
}
