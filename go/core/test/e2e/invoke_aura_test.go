package e2e_test

import (
	"os"
	"testing"

	"github.com/kagent-dev/kagent/go/api/v1alpha2"
)

// TestE2EInvokeAuraAgent exercises a declarative agent with runtime "aura":
// kagent renders the AURA TOML, deploys the mezmo/aura image, and the agent is
// invoked over A2A. The mock LLM stands in for the model (via the ModelConfig's
// OpenAI base_url, which the aura translator now emits).
//
// This test pulls the third-party mezmo/aura image and depends on AURA's A2A
// interop, so it is opt-in: set KAGENT_E2E_AURA=1 to run it. This keeps the
// default CI pipeline independent of an external image until interop is
// confirmed by a maintainer.
func TestE2EInvokeAuraAgent(t *testing.T) {
	if os.Getenv("KAGENT_E2E_AURA") != "1" {
		t.Skip("set KAGENT_E2E_AURA=1 to run the AURA runtime e2e test (pulls mezmo/aura image)")
	}

	// Mock LLM returns "pong from aura" for a "ping" prompt.
	baseURL, stopServer := setupMockServer(t, "mocks/invoke_aura_agent.json")
	defer stopServer()

	cli := setupK8sClient(t, false)

	// ModelConfig points OpenAI at the mock server; the aura translator emits
	// base_url so AURA talks to it.
	modelCfg := setupModelConfig(t, cli, baseURL)

	auraRuntime := v1alpha2.DeclarativeRuntime_Aura
	agent := setupAgentWithOptions(t, cli, modelCfg.Name, nil, AgentOptions{
		Name:          "test-aura-agent",
		SystemMessage: "You are an AURA test agent.",
		Runtime:       &auraRuntime,
	})

	a2aClient := setupA2AClient(t, agent)

	t.Run("sync_invocation", func(t *testing.T) {
		runSyncTest(t, a2aClient, "ping", "pong from aura", nil)
	})
}
