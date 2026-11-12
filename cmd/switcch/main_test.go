//go:build darwin

package main

import "testing"

func TestRoute(t *testing.T) {
	cases := []struct {
		args      []string
		kind, ref string
		wantErr   bool
	}{
		{args: []string{"add"}, kind: "add"},
		{args: []string{"accounts"}, kind: "list"},
		{args: []string{"list"}, kind: "list"},
		{args: []string{"remove", "work"}, kind: "remove", ref: "work"},
		{args: []string{"work"}, kind: "launch", ref: "work"},
		{args: []string{}, wantErr: true},
		{args: []string{"remove"}, wantErr: true}, // remove needs a ref
	}
	for _, c := range cases {
		cmd, err := route(c.args)
		if c.wantErr {
			if err == nil {
				t.Fatalf("args %v: want usage error", c.args)
			}
			continue
		}
		if err != nil {
			t.Fatalf("args %v: unexpected error %v", c.args, err)
		}
		if cmd.kind != c.kind {
			t.Fatalf("args %v: kind = %q, want %q", c.args, cmd.kind, c.kind)
		}
		if cmd.ref != c.ref {
			t.Fatalf("args %v: ref = %q, want %q", c.args, cmd.ref, c.ref)
		}
	}
}

func TestRouteAddAlias(t *testing.T) {
	cmd, err := route([]string{"add", "--alias", "work"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.alias != "work" {
		t.Fatalf("alias = %q, want work", cmd.alias)
	}
}

func TestRouteLaunchForwardsClaudeArgs(t *testing.T) {
	cmd, err := route([]string{"work", "--resume"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.kind != "launch" || cmd.ref != "work" {
		t.Fatalf("got %+v", cmd)
	}
	if len(cmd.claudeArgs) != 1 || cmd.claudeArgs[0] != "--resume" {
		t.Fatalf("claudeArgs = %v, want [--resume]", cmd.claudeArgs)
	}
}
