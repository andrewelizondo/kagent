package aura

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalProvider(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "openai", want: "OpenAI", ok: true},
		{in: "OpenAI", want: "OpenAI", ok: true},
		{in: "ANTHROPIC", want: "Anthropic", ok: true},
		{in: "gemini", want: "Gemini", ok: true},
		{in: "bedrock", want: "Bedrock", ok: true},
		{in: "ollama", want: "Ollama", ok: true},
		{in: "azure", ok: false},
		{in: "", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := CanonicalProvider(tt.in)
			if ok != tt.ok || got != tt.want {
				t.Errorf("CanonicalProvider(%q) = (%q,%v), want (%q,%v)", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestGenerateManifests(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		model        string
		wantContains []string
		wantAbsent   []string
	}{
		{
			name:     "openai uses api key secret",
			provider: "OpenAI",
			model:    "gpt-5.2",
			wantContains: []string{
				"kind: Secret",
				"api-key: \"REPLACE_WITH_API_KEY\"",
				"provider: OpenAI",
				"model: gpt-5.2",
				"apiKeySecretKey: api-key",
				"runtime: aura",
			},
			wantAbsent: []string{"bedrock:", "ollama:"},
		},
		{
			name:     "bedrock uses aws creds and region",
			provider: "Bedrock",
			model:    "us.anthropic.claude-3-5-sonnet-20241022-v2:0",
			wantContains: []string{
				"AWS_ACCESS_KEY_ID:",
				"provider: Bedrock",
				"bedrock:",
				"region: us-east-1",
			},
			wantAbsent: []string{"api-key:", "apiKeySecretKey:"},
		},
		{
			name:     "ollama has no secret and a host",
			provider: "Ollama",
			model:    "qwen3:30b-a3b",
			wantContains: []string{
				"provider: Ollama",
				"ollama:",
				"host:",
			},
			wantAbsent: []string{"kind: Secret", "api-key:"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			g := NewAuraGenerator()
			if err := g.Generate(dir, "myagent", "", tt.provider, tt.model, "", false, ""); err != nil {
				t.Fatalf("Generate() error: %v", err)
			}

			data, err := os.ReadFile(filepath.Join(dir, "manifests.yaml"))
			if err != nil {
				t.Fatalf("read manifests.yaml: %v", err)
			}
			got := string(data)
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("manifests missing %q\n--- got ---\n%s", want, got)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("manifests unexpectedly contains %q\n--- got ---\n%s", absent, got)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
				t.Errorf("expected README.md: %v", err)
			}
		})
	}
}

func TestGenerateRejectsUnsupportedProvider(t *testing.T) {
	g := NewAuraGenerator()
	if err := g.Generate(t.TempDir(), "x", "", "azure", "m", "", false, ""); err == nil {
		t.Fatal("expected error for unsupported provider, got nil")
	}
}
