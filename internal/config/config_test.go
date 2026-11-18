package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func TestLoadValidates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	p := filepath.Join(dir, "switcch", "config.json")
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	for body, key := range map[string]string{`{"mode":"Auto"}`: "mode", `{"threshold":0}`: "threshold", `{"trigger":"often"}`: "trigger", `{"idleMigrateMinutes":-1}`: "idleMigrateMinutes"} {
		_ = os.WriteFile(p, []byte(body), 0o600)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("%s: want an error naming %s, got %v", body, key, err)
		}
	}
	_ = os.Remove(p)
	c, err := Load()
	if err != nil || !slices.Equal(c.ModelFallback, []string{"sonnet"}) || c.WaitForReset || c.EventCommand != "" ||
		c.ModelNotice != "[switcch] {account} hit its {from} limit — continuing with {to}." ||
		c.ResumeMessage != "[switcch] The usage limit has reset — continue where you left off." {
		t.Fatalf("defaults: %+v %v", c, err)
	}
}

func TestSetNewKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Set("modelFallback", ""); err != nil {
		t.Fatal(err)
	}
	if c, _ := Load(); len(c.ModelFallback) != 0 {
		t.Fatalf(`modelFallback "" must persist as empty (fallback off), got %v`, c.ModelFallback)
	}
	if err := Set("modelFallback", "sonnet, haiku"); err != nil {
		t.Fatal(err)
	}
	if err := Set("waitForReset", "true"); err != nil {
		t.Fatal(err)
	}
	if err := Set("eventCommand", "cat >> /tmp/e"); err != nil {
		t.Fatal(err)
	}
	c, _ := Load()
	if !slices.Equal(c.ModelFallback, []string{"sonnet", "haiku"}) || !c.WaitForReset || c.EventCommand != "cat >> /tmp/e" {
		t.Fatalf("persisted: %+v", c)
	}
	if err := Set("waitForReset", "maybe"); err == nil {
		t.Fatal("waitForReset must be true or false")
	}
}
