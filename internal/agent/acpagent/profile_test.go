package acpagent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuiltInProfiles(t *testing.T) {
	opencode := OpenCodeProfile("opencode")
	if len(opencode.Args) != 1 || opencode.Args[0] != "acp" {
		t.Fatalf("unexpected OpenCode args: %#v", opencode.Args)
	}
	grok := GrokProfile("grok")
	want := []string{"--no-auto-update", "agent", "stdio"}
	if len(grok.Args) != len(want) {
		t.Fatalf("unexpected Grok args: %#v", grok.Args)
	}
	for i := range want {
		if grok.Args[i] != want[i] {
			t.Fatalf("Grok args[%d] = %q, want %q", i, grok.Args[i], want[i])
		}
	}
	if !grok.AuthOptional {
		t.Fatal("Grok authentication should allow BYOK provider fallback")
	}
}

func TestLoadProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	contents := `{"agents":[{"name":"internal","command":"internal-agent","args":["acp"],"auth_methods":["token"],"auth_optional":true}]}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, err := LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].Name != "internal" || profiles[0].Command != "internal-agent" || !profiles[0].AuthOptional {
		t.Fatalf("unexpected profiles: %#v", profiles)
	}
}

func TestLoadProfilesRejectsUnknownAndDuplicateEntries(t *testing.T) {
	for name, contents := range map[string]string{
		"unknown":   `{"agents":[{"name":"a","command":"agent","api_key":"secret"}]}`,
		"duplicate": `{"agents":[{"name":"a","command":"one"},{"name":"a","command":"two"}]}`,
		"trailing":  `{"agents":[{"name":"a","command":"one"}]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agents.json")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadProfiles(path); err == nil {
				t.Fatal("invalid profile file was accepted")
			}
		})
	}
}
