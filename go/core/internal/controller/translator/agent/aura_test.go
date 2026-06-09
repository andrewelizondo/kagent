package agent

import (
	"strings"
	"testing"

	"github.com/kagent-dev/kagent/go/api/v1alpha2"
)

func TestAuraProviderConfig(t *testing.T) {
	tests := []struct {
		name         string
		provider     v1alpha2.ModelProvider
		wantProvider string
		wantEnv      string
		wantOK       bool
	}{
		{name: "openai", provider: v1alpha2.ModelProviderOpenAI, wantProvider: "openai", wantEnv: "OPENAI_API_KEY", wantOK: true},
		{name: "anthropic", provider: v1alpha2.ModelProviderAnthropic, wantProvider: "anthropic", wantEnv: "ANTHROPIC_API_KEY", wantOK: true},
		{name: "unsupported azure", provider: v1alpha2.ModelProviderAzureOpenAI, wantOK: false},
		{name: "unsupported bedrock", provider: v1alpha2.ModelProviderBedrock, wantOK: false},
		{name: "unsupported gemini", provider: v1alpha2.ModelProviderGemini, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotProvider, gotEnv, gotOK := auraProviderConfig(tt.provider)
			if gotOK != tt.wantOK {
				t.Fatalf("auraProviderConfig(%q) ok = %v, want %v", tt.provider, gotOK, tt.wantOK)
			}
			if gotProvider != tt.wantProvider {
				t.Errorf("auraProviderConfig(%q) provider = %q, want %q", tt.provider, gotProvider, tt.wantProvider)
			}
			if gotEnv != tt.wantEnv {
				t.Errorf("auraProviderConfig(%q) env = %q, want %q", tt.provider, gotEnv, tt.wantEnv)
			}
		})
	}
}

func TestTomlString(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "hello", want: `"hello"`},
		{name: "quotes", in: `say "hi"`, want: `"say \"hi\""`},
		{name: "backslash", in: `a\b`, want: `"a\\b"`},
		{name: "newline", in: "line1\nline2", want: `"line1\nline2"`},
		{name: "tab and cr", in: "a\tb\rc", want: `"a\tb\rc"`},
		{name: "control char", in: "a\x01b", want: `"a\u0001b"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tomlString(tt.in); got != tt.want {
				t.Errorf("tomlString(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeTOMLKey(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "mezmo-telemetry", want: "mezmo-telemetry"},
		{in: "my server.name", want: "my_server_name"},
		{in: "alpha123_X", want: "alpha123_X"},
		{in: "", want: "server"},
		{in: "with/slash:colon", want: "with_slash_colon"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := sanitizeTOMLKey(tt.in); got != tt.want {
				t.Errorf("sanitizeTOMLKey(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRenderAuraConfigTOML(t *testing.T) {
	got := renderAuraConfigTOML(auraConfigInput{
		Name:         "aura-sre",
		SystemPrompt: "You are an SRE.\nInvestigate alerts.",
		Provider:     "openai",
		APIKeyEnv:    "OPENAI_API_KEY",
		Model:        "gpt-5.2",
		Servers: []auraMCPServer{
			{
				Name:      "mezmo-telemetry",
				Transport: "http_streamable",
				URL:       "http://mezmo.kagent.svc:8080/mcp",
				Headers:   map[string]string{"Authorization": "Bearer t", "X-Tenant": "acme"},
			},
		},
	})

	wantContains := []string{
		"[agent]",
		`name = "aura-sre"`,
		`system_prompt = "You are an SRE.\nInvestigate alerts."`,
		"[agent.llm]",
		`provider = "openai"`,
		`api_key = "{{ env.OPENAI_API_KEY }}"`,
		`model = "gpt-5.2"`,
		"[mcp.servers.mezmo-telemetry]",
		`transport = "http_streamable"`,
		`url = "http://mezmo.kagent.svc:8080/mcp"`,
		// headers are sorted by key for determinism
		`headers = { "Authorization" = "Bearer t", "X-Tenant" = "acme" }`,
	}
	for _, want := range wantContains {
		if !strings.Contains(got, want) {
			t.Errorf("rendered TOML missing %q\n--- got ---\n%s", want, got)
		}
	}
}

func TestRenderAuraConfigTOML_NoServers(t *testing.T) {
	got := renderAuraConfigTOML(auraConfigInput{
		Name:         "minimal",
		SystemPrompt: "hi",
		Provider:     "anthropic",
		APIKeyEnv:    "ANTHROPIC_API_KEY",
		Model:        "claude-x",
	})
	if strings.Contains(got, "[mcp.servers") {
		t.Errorf("expected no mcp.servers section, got:\n%s", got)
	}
	if !strings.Contains(got, `provider = "anthropic"`) {
		t.Errorf("expected anthropic provider, got:\n%s", got)
	}
}
