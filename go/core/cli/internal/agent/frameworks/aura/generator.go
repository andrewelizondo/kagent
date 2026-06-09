// Package aura scaffolds a declarative kagent Agent that runs on the Mezmo AURA
// runtime. Unlike the ADK frameworks, an aura agent is fully declarative — the
// generator emits Kubernetes manifests (ModelConfig + Agent, plus a Secret stub)
// rather than a buildable project.
package aura

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AuraGenerator generates manifests for a declarative agent with runtime: aura.
type AuraGenerator struct{}

// NewAuraGenerator creates a new AURA manifest generator.
func NewAuraGenerator() *AuraGenerator {
	return &AuraGenerator{}
}

// CanonicalProvider maps a case-insensitive provider name to the kagent
// ModelConfig enum value supported by the aura runtime. The bool reports support.
func CanonicalProvider(provider string) (string, bool) {
	switch strings.ToLower(provider) {
	case "openai":
		return "OpenAI", true
	case "anthropic":
		return "Anthropic", true
	case "gemini":
		return "Gemini", true
	case "bedrock":
		return "Bedrock", true
	case "ollama":
		return "Ollama", true
	default:
		return "", false
	}
}

const defaultSystemMessage = "You are a helpful assistant. Use the available tools to complete the user's request."

// Generate writes manifests.yaml and README.md into projectDir. The signature
// matches the frameworks.Generator interface; instruction (when provided) is
// used as the agent's system message, and language/kagentVersion are unused.
func (g *AuraGenerator) Generate(projectDir, agentName, instruction, modelProvider, modelName, description string, verbose bool, _ string) error {
	provider, ok := CanonicalProvider(modelProvider)
	if !ok {
		return fmt.Errorf("unsupported model provider %q for aura. Supported: OpenAI, Anthropic, Gemini, Bedrock, Ollama", modelProvider)
	}

	systemMessage := strings.TrimSpace(instruction)
	if systemMessage == "" {
		systemMessage = defaultSystemMessage
	}
	if description == "" {
		description = fmt.Sprintf("Mezmo AURA agent %q", agentName)
	}

	manifests := renderManifests(agentName, provider, modelName, description, systemMessage)
	if err := os.WriteFile(filepath.Join(projectDir, "manifests.yaml"), []byte(manifests), 0644); err != nil {
		return fmt.Errorf("failed to write manifests.yaml: %v", err)
	}

	readme := renderReadme(agentName, provider, modelName)
	if err := os.WriteFile(filepath.Join(projectDir, "README.md"), []byte(readme), 0644); err != nil {
		return fmt.Errorf("failed to write README.md: %v", err)
	}

	fmt.Printf("✅ Successfully scaffolded AURA agent %q in %s\n", agentName, projectDir)
	fmt.Printf("🤖 Model: %s (%s)\n", provider, modelName)
	fmt.Printf("📁 Files:\n   %s/\n   ├── manifests.yaml\n   └── README.md\n", agentName)
	fmt.Printf("\n🚀 Next steps:\n")
	switch provider {
	case "Bedrock":
		fmt.Printf("   1. Edit manifests.yaml: set AWS credentials in the Secret and the region.\n")
	case "Ollama":
		fmt.Printf("   1. Edit manifests.yaml: set the Ollama host URL.\n")
	default:
		fmt.Printf("   1. Edit manifests.yaml: set a real API key in the Secret.\n")
	}
	fmt.Printf("   2. kubectl apply -f %s/manifests.yaml\n", agentName)
	fmt.Printf("   3. kagent invoke -t \"hello\" --agent %s --stream\n", agentName)
	return nil
}

// usesAPIKey reports whether the provider authenticates with a single API key.
func usesAPIKey(provider string) bool {
	switch provider {
	case "OpenAI", "Anthropic", "Gemini":
		return true
	default: // Bedrock (AWS creds), Ollama (none)
		return false
	}
}

func renderManifests(agentName, provider, modelName, description, systemMessage string) string {
	var b strings.Builder

	b.WriteString("# Scaffolded by `kagent init aura`. Declarative agent on the Mezmo AURA runtime.\n")
	b.WriteString(fmt.Sprintf("# Apply with: kubectl apply -f manifests.yaml\n\n"))

	// Secret (provider-dependent).
	switch {
	case usesAPIKey(provider):
		b.WriteString("apiVersion: v1\nkind: Secret\n")
		b.WriteString(fmt.Sprintf("metadata:\n  name: %s-model\n  namespace: kagent\n", agentName))
		b.WriteString("type: Opaque\nstringData:\n  api-key: \"REPLACE_WITH_API_KEY\"\n---\n")
	case provider == "Bedrock":
		b.WriteString("apiVersion: v1\nkind: Secret\n")
		b.WriteString(fmt.Sprintf("metadata:\n  name: %s-model\n  namespace: kagent\n", agentName))
		b.WriteString("type: Opaque\nstringData:\n  AWS_ACCESS_KEY_ID: \"REPLACE\"\n  AWS_SECRET_ACCESS_KEY: \"REPLACE\"\n---\n")
		// Ollama: no Secret.
	}

	// ModelConfig.
	b.WriteString("apiVersion: kagent.dev/v1alpha2\nkind: ModelConfig\n")
	b.WriteString(fmt.Sprintf("metadata:\n  name: %s-model\n  namespace: kagent\n", agentName))
	b.WriteString("spec:\n")
	b.WriteString(fmt.Sprintf("  provider: %s\n", provider))
	b.WriteString(fmt.Sprintf("  model: %s\n", modelName))
	switch {
	case usesAPIKey(provider):
		b.WriteString(fmt.Sprintf("  apiKeySecret: %s-model\n  apiKeySecretKey: api-key\n", agentName))
	case provider == "Bedrock":
		b.WriteString(fmt.Sprintf("  apiKeySecret: %s-model\n", agentName))
		b.WriteString("  bedrock:\n    region: us-east-1\n")
	case provider == "Ollama":
		b.WriteString("  ollama:\n    host: http://ollama.ollama.svc:11434\n")
	}
	b.WriteString("---\n")

	// Agent.
	b.WriteString("apiVersion: kagent.dev/v1alpha2\nkind: Agent\n")
	b.WriteString(fmt.Sprintf("metadata:\n  name: %s\n  namespace: kagent\n", agentName))
	b.WriteString("spec:\n  type: Declarative\n")
	b.WriteString(fmt.Sprintf("  description: %q\n", description))
	b.WriteString("  declarative:\n    runtime: aura\n")
	b.WriteString(fmt.Sprintf("    modelConfig: %s-model\n", agentName))
	b.WriteString("    systemMessage: |\n")
	for _, line := range strings.Split(systemMessage, "\n") {
		b.WriteString("      " + line + "\n")
	}
	b.WriteString("    # Add MCP tools (uncomment and point at a RemoteMCPServer / MCPServer):\n")
	b.WriteString("    # tools:\n")
	b.WriteString("    #   - type: McpServer\n")
	b.WriteString("    #     mcpServer:\n")
	b.WriteString("    #       name: my-tools\n")
	b.WriteString("    #       apiGroup: kagent.dev\n")
	b.WriteString("    #       kind: RemoteMCPServer\n")
	b.WriteString("    # AURA-only features (orchestration, vector stores) via a raw-TOML overlay:\n")
	b.WriteString("    # auraConfigFrom:\n")
	b.WriteString("    #   type: ConfigMap\n")
	b.WriteString(fmt.Sprintf("    #   name: %s-overrides\n", agentName))
	b.WriteString("    #   key: overrides.toml\n")

	return b.String()
}

func renderReadme(agentName, provider, modelName string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# %s — Mezmo AURA agent\n\n", agentName))
	b.WriteString("A declarative kagent `Agent` running on the **AURA** runtime. kagent renders AURA's\n")
	b.WriteString("TOML from this spec and deploys the AURA image; the agent is served over A2A like\n")
	b.WriteString("any other kagent agent.\n\n")
	b.WriteString(fmt.Sprintf("- **Model:** %s (`%s`)\n\n", provider, modelName))
	b.WriteString("## Deploy\n\n")
	switch provider {
	case "Bedrock":
		b.WriteString("1. Edit `manifests.yaml`: set AWS credentials in the Secret and the `region`.\n")
	case "Ollama":
		b.WriteString("1. Edit `manifests.yaml`: set the Ollama `host` URL.\n")
	default:
		b.WriteString("1. Edit `manifests.yaml`: set a real API key in the Secret.\n")
	}
	b.WriteString("2. `kubectl apply -f manifests.yaml`\n")
	b.WriteString(fmt.Sprintf("3. `kubectl get agent -n kagent %s -w` until Ready\n", agentName))
	b.WriteString(fmt.Sprintf("4. `kagent invoke -t \"hello\" --agent %s --stream`\n\n", agentName))
	b.WriteString("## Add tools\n\n")
	b.WriteString("Uncomment the `tools` block and reference a `RemoteMCPServer` or `MCPServer`.\n")
	b.WriteString("For AURA-only features (orchestration, RAG vector stores), provide extra TOML via\n")
	b.WriteString("`auraConfigFrom` pointing at a ConfigMap key.\n")
	return b.String()
}
