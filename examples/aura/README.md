# Mezmo AURA on kagent

[Mezmo AURA](https://github.com/mezmo/aura) is an open-source agentic harness for
production AI / SRE workflows: it composes agents from declarative TOML, integrates tools
over MCP, supports RAG and multi-agent orchestration, and exposes an OpenAI-compatible API
— plus an **A2A** interface when `AURA_ENABLE_A2A=true`.

That A2A surface is what lets AURA run on kagent as a **BYO agent today, with no controller
changes**. AURA's agent card is served at `/.well-known/agent-card.json` on port `8080`,
exactly where kagent's BYO readiness probe looks.

This directory ships the **Phase 0** integration. The broader design — including a proposed
first-class `runtime: aura` that renders AURA's TOML from a normal declarative `Agent`
(reusing `ModelConfig` and `RemoteMCPServer`) — lives in
[`design/EP-aura-runtime.md`](../../design/EP-aura-runtime.md).

## Prerequisites

- A cluster with kagent installed (`kagent install` / `make helm-install`).
- An LLM API key for your provider.

## Run it

1. Edit `aura-byo-agent.yaml` and set a real key in the `aura-llm` Secret (and adjust the
   `[agent.llm]` provider/model in the ConfigMap if you are not using OpenAI).

2. Apply the manifests:

   ```bash
   kubectl apply -n kagent -f aura-byo-agent.yaml
   ```

3. Wait for the agent to become Ready (the readiness probe hits AURA's agent card):

   ```bash
   kubectl get agent -n kagent aura-sre -w
   ```

4. Invoke it like any other kagent agent:

   ```bash
   kagent invoke -t "Summarize what you can help an SRE with" --agent aura-sre --stream
   ```

   It also appears in the dashboard (`kagent dashboard`) and is discoverable over A2A at
   `…:8083/api/a2a/kagent/aura-sre/`.

## What the manifests do

| Object | Purpose |
|--------|---------|
| `Secret/aura-llm` | Holds the LLM API key. Injected as an env var; never written into the ConfigMap. |
| `ConfigMap/aura-sre-config` | AURA's TOML config. Secrets use `{{ env.VAR }}` placeholders. |
| `Agent/aura-sre` (`type: BYO`) | Runs `mezmo/aura:1-latest` with `AURA_ENABLE_A2A=true`, mounts the config, and exposes the agent over A2A on port 8080. |

## Bidirectional MCP

The example config points AURA's `[mcp.servers.kagent]` at the kagent controller's `/mcp`
endpoint, so the AURA agent can call **other kagent agents** as tools (`list_agents` /
`invoke_agent`). Swap in the Mezmo telemetry MCP server, runbooks, or ticketing — or model
them as kagent `RemoteMCPServer`s — to give AURA real SRE capabilities.

Because the AURA agent is itself an A2A `Agent`, other kagent agents can also use it as a
`type: Agent` tool. AURA and kagent agents become peers in the same mesh.

## Limitations (Phase 0)

- The model/provider and MCP servers are described in TOML here rather than reused from a
  kagent `ModelConfig` / `RemoteMCPServer`. The proposed `runtime: aura`
  ([EP](../../design/EP-aura-runtime.md)) removes that duplication.
- AURA-only features (orchestration workers, vector stores) are configured directly in the
  TOML — point `CONFIG_PATH` at a directory and add more TOML files as needed.
