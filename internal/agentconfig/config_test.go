package agentconfig

import (
	"os"
	"path/filepath"
	"testing"

	"cyber-foreman/internal/agent"
)

func TestLoadAndBuildConfiguredAgents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	contents := `{"agents":[
		{"name":"internal-acp","driver":"acp","command":"agent","args":["acp"],"default_workspace":"/work","default_model":"qwen3.8","provider":{"format":"openai","base_url":"http://models.internal/v1","api_key_env":"MODEL_KEY"}},
		{"name":"internal-codex","driver":"codex-app-server","command":"codex","args":["app-server"],"default_model":"qwen3.8","provider":{"format":"anthropic","base_url":"http://models.internal/anthropic","api_key_env":"MODEL_KEY"}}
	]}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 {
		t.Fatalf("profiles=%d, want 2", len(profiles))
	}
	for _, profile := range profiles {
		adapter, buildErr := Build(profile)
		if buildErr != nil {
			t.Fatalf("build %s: %v", profile.Name, buildErr)
		}
		metadata := agent.MetadataOf(adapter)
		if !metadata.Selectable || metadata.DefaultModel != "qwen3.8" || metadata.ProviderFormat == "" {
			t.Fatalf("unexpected metadata for %s: %#v", profile.Name, metadata)
		}
	}
}

func TestLoadRejectsUnsafeOrAmbiguousProfiles(t *testing.T) {
	for name, contents := range map[string]string{
		"unknown-field":      `{"agents":[{"name":"a","driver":"acp","command":"agent","api_key":"secret"}]}`,
		"duplicate":          `{"agents":[{"name":"a","driver":"acp","command":"one"},{"name":"a","driver":"acp","command":"two"}]}`,
		"unknown-format":     `{"agents":[{"name":"a","driver":"acp","command":"agent","default_model":"m","provider":{"format":"ollama"}}]}`,
		"codex-no-base":      `{"agents":[{"name":"codex","driver":"codex-app-server","command":"codex","default_model":"m","provider":{"format":"openai"}}]}`,
		"literal-key":        `{"agents":[{"name":"a","driver":"acp","command":"agent","default_model":"m","provider":{"format":"openai","api_key":"secret"}}]}`,
		"relative-workspace": `{"agents":[{"name":"a","driver":"acp","command":"agent","default_workspace":"relative"}]}`,
		"invalid-key-env":    `{"agents":[{"name":"a","driver":"acp","command":"agent","default_model":"m","provider":{"format":"openai","api_key_env":"BAD=KEY"}}]}`,
		"google-responses":   `{"agents":[{"name":"a","driver":"codex-app-server","command":"codex","default_model":"m","provider":{"format":"google","wire_api":"responses","base_url":"https://example.com"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agents.json")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}
}

func TestGoogleCodexUsesChatCompatibility(t *testing.T) {
	adapter, err := Build(Profile{
		Name: "codex", Driver: DriverCodex, Command: "codex", DefaultModel: "gemini",
		Provider: &Provider{Format: FormatGoogle, BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if metadata := agent.MetadataOf(adapter); metadata.ProviderFormat != FormatGoogle {
		t.Fatalf("metadata=%#v", metadata)
	}
}

func TestLoadFileIncludesSecurityBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	contents := `{"agents":[{"name":"a","driver":"acp","command":"agent"}],"security":{"workspace_roots":["/srv/code"],"command_allowlist":[["go","test"],["./scripts/test"]],"api_token_env":"FOREMAN_API_TOKEN"}}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Security == nil || config.Security.APITokenEnv != "FOREMAN_API_TOKEN" || len(config.Security.CommandAllowlist) != 2 {
		t.Fatalf("security=%#v", config.Security)
	}
}

func TestLoadFileRejectsInvalidSecurity(t *testing.T) {
	for name, security := range map[string]string{
		"relative-root": `{"workspace_roots":["relative"]}`,
		"empty-prefix":  `{"command_allowlist":[[]]}`,
		"invalid-env":   `{"api_token_env":"BAD=TOKEN"}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agents.json")
			contents := `{"agents":[{"name":"a","driver":"acp","command":"agent"}],"security":` + security + `}`
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFile(path); err == nil {
				t.Fatal("invalid security configuration was accepted")
			}
		})
	}
}

func TestLoadFileIncludesSemanticReviewer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	contents := `{"agents":[{"name":"a","driver":"acp","command":"agent"}],"supervisor":{"semantic_review":{"format":"openai","base_url":"https://api.deepseek.com/v1","api_key_env":"FOREMAN_AGENT_API_KEY_OPENAI","model":"deepseek-chat","timeout":"15s","tool_calling":true,"allow_workspace_diff":true,"allow_operator_attention":true}}}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reviewer := config.Supervisor.SemanticReview
	if reviewer.Format != FormatOpenAI || reviewer.Model != "deepseek-chat" || reviewer.TimeoutDuration().String() != "15s" || !reviewer.ToolCalling || !reviewer.AllowWorkspaceDiff || !reviewer.AllowAttention {
		t.Fatalf("semantic reviewer=%#v", reviewer)
	}
}

func TestLoadFileRejectsInvalidSemanticReviewer(t *testing.T) {
	for name, reviewer := range map[string]string{
		"format":   `{"format":"anthropic","base_url":"https://example.com","api_key_env":"KEY","model":"m"}`,
		"base":     `{"format":"openai","base_url":"relative","api_key_env":"KEY","model":"m"}`,
		"userinfo": `{"format":"openai","base_url":"https://user:pass@example.com","api_key_env":"KEY","model":"m"}`,
		"key env":  `{"format":"openai","base_url":"https://example.com","api_key_env":"BAD=KEY","model":"m"}`,
		"model":    `{"format":"openai","base_url":"https://example.com","api_key_env":"KEY"}`,
		"timeout":  `{"format":"openai","base_url":"https://example.com","api_key_env":"KEY","model":"m","timeout":"never"}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agents.json")
			contents := `{"agents":[{"name":"a","driver":"acp","command":"agent"}],"supervisor":{"semantic_review":` + reviewer + `}}`
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFile(path); err == nil {
				t.Fatal("invalid semantic reviewer was accepted")
			}
		})
	}
}
