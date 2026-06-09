# Mezmo AURA on kagent

[Mezmo AURA](https://github.com/mezmo/aura) is an open-source agentic harness for
production AI / SRE workflows: it composes agents from declarative TOML, integrates tools
over MCP, supports RAG and multi-agent orchestration, and exposes an OpenAI-compatible API
— plus an **A2A** interface when `AURA_ENABLE_A2A=true`.

That A2A surface (agent card at `/.well-known/agent-card.json` on port `8080`, exactly where
kagent's readiness probe looks) is what makes AURA a clean fit for kagent.

This directory ships two ways to run AURA, both backed by the design in
[`design/EP-aura-runtime.md`](../../design/EP-aura-runtime.md):

| File | Phase | What it is |
|------|-------|-----------|
| `aura-runtime-agent.yaml` | 1 (recommended) | A declarative `Agent` with `runtime: aura`. kagent renders the TOML from the spec, reusing `ModelConfig` and `RemoteMCPServer`. |
| `aura-byo-agent.yaml` | 0 | A `type: BYO` agent running the AURA image with a hand-authored TOML ConfigMap. No controller support required; useful for AURA-only features (orchestration, RAG) not yet exposed by the runtime. |

## Phase 1 — first-class `runtime: aura` (recommended)

Write an ordinary declarative `Agent`; kagent generates AURA's TOML, deploys the AURA image
with A2A enabled, and injects the model API key from the `ModelConfig` Secret:

```bash
kubectl apply -n kagent -f aura-runtime-agent.yaml
kubectl get agent -n kagent aura-sre -w
kagent invoke -t "What can you help an SRE with?" --agent aura-sre --stream
```

Supported model providers for the aura runtime are **OpenAI, Anthropic, Gemini, Bedrock,
and Ollama**. OpenAI may set a custom `base_url` (self-hosted / proxy / OpenRouter). The
AURA image defaults to `mezmo/aura:1-latest` and is configurable via the
`controller.auraImage` Helm value (or the controller `--aura-image` flag).

### Scaffold one with the CLI

```bash
kagent init aura sre --model-provider Anthropic --model-name claude-sonnet-4-20250514
# writes sre/manifests.yaml (Secret + ModelConfig + Agent) and sre/README.md
```

### AURA-only features (orchestration, RAG)

Features without a kagent field (`turn_depth`, `[orchestration]`, `[[vector_stores]]`) are
supplied as raw TOML via `auraConfigFrom`, which is appended to the generated config:

```yaml
  declarative:
    runtime: aura
    modelConfig: aura-model
    systemMessage: "…"
    auraConfigFrom:
      type: ConfigMap   # or Secret
      name: aura-sre-overrides
      key: overrides.toml
```

```yaml
# ConfigMap aura-sre-overrides, key overrides.toml
[orchestration]
enabled = true
max_planning_cycles = 3
```

## Phase 0 — AURA as a BYO agent

Use this when you need AURA-only features the runtime does not yet expose (orchestration
workers, vector stores), or a provider the runtime does not yet support. You author the TOML
yourself in a ConfigMap and run the AURA image as a `type: BYO` agent:

1. Edit `aura-byo-agent.yaml` and set a real key in the `aura-llm` Secret (and adjust the
   `[agent.llm]` provider/model in the ConfigMap if you are not using OpenAI).
2. Apply and invoke:

   ```bash
   kubectl apply -n kagent -f aura-byo-agent.yaml
   kubectl get agent -n kagent aura-sre-byo -w
   kagent invoke -t "Summarize what you can help an SRE with" --agent aura-sre-byo --stream
   ```

| Object | Purpose |
|--------|---------|
| `Secret/aura-llm` | Holds the LLM API key. Injected as an env var; never written into the ConfigMap. |
| `ConfigMap/aura-sre-byo-config` | AURA's TOML config. Secrets use `{{ env.VAR }}` placeholders. |
| `Agent/aura-sre-byo` (`type: BYO`) | Runs `mezmo/aura:1-latest` with `AURA_ENABLE_A2A=true`, mounts the config, and exposes the agent over A2A on port 8080. |

## Bidirectional MCP

Both examples point AURA at the kagent controller's `/mcp` endpoint, so the AURA agent can
call **other kagent agents** as tools (`list_agents` / `invoke_agent`). Swap in the Mezmo
telemetry MCP server, runbooks, or ticketing to give AURA real SRE capabilities.

Because an AURA agent is itself an A2A `Agent`, other kagent agents can also use it as a
`type: Agent` tool. AURA and kagent agents become peers in the same mesh.
