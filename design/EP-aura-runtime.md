# EP-aura-runtime: First-class support for Mezmo AURA agents

* Issue: TBD (kagent-dev/kagent)
* Status: provisional (Phase 0, 1, and 2 implemented)

## Background

[Mezmo AURA](https://github.com/mezmo/aura) is an open-source ("System of Context")
agentic harness for production AI, aimed primarily at SRE / agentic-ops workflows
(root-cause analysis, drift detection, deployment checks). It is written in Rust and:

- Composes agents from **declarative TOML** (`[agent]`, `[agent.llm]`, `[[vector_stores]]`,
  `[mcp.servers.*]`, `[orchestration]`).
- Integrates tools over **MCP** (`http_streamable`, `sse`, `stdio` transports).
- Supports **RAG / vector search** and multi-agent **orchestration** (planner + workers).
- Exposes an **OpenAI-compatible** REST/SSE API on port `8080`
  (`/v1/models`, `/v1/chat/completions`, `/health`) and, when `AURA_ENABLE_A2A=true`,
  an **A2A** interface (`/.well-known/agent-card.json`, `/a2a/v1/message:send`,
  `/a2a/v1/rpc`) on the same port.
- Reads config from `CONFIG_PATH` (a single TOML file or a directory of them) with
  `{{ env.VAR }}` substitution for secrets. Ships as `mezmo/aura:1-latest`.

kagent is a Kubernetes-native control plane for agents. It already models three agent
shapes in `kagent.dev/v1alpha2`:

- **Declarative** agents — fully described by the CRD (model, instructions, tools),
  running on a built-in runtime (`python` or `go` ADK).
- **BYO** agents — a user container that serves **A2A on port 8080**.
- **AgentHarness** — a remote execution environment provisioned by a pluggable backend
  (`openclaw`, `nemoclaw`, `hermes`) over `openshell`/`substrate` runtimes.

The two systems are complementary: AURA is an *agent harness*, kagent is a *fleet control
plane*. AURA's A2A surface lines up exactly with kagent's BYO contract — kagent's BYO
readiness probe targets `/.well-known/agent-card.json`
(`go/core/internal/controller/translator/agent/manifest_builder.go:523`), the same path
AURA serves when A2A is enabled. That alignment is what makes a clean integration possible.

This EP proposes how kagent should support AURA agents, what the *ideal* user experience
is, and how that experience scales to many agents and tenants.

## Motivation

Users who have standardized on kagent for fleet management, RBAC, secrets, observability,
and A2A/MCP interconnect want to run AURA's SRE agents without:

- building and maintaining bespoke container images per agent,
- duplicating LLM-provider/secret config that already lives in a kagent `ModelConfig`,
- re-plumbing MCP endpoints that are already modeled as `RemoteMCPServer`/`MCPServer`,
- losing the kagent dashboard, `kagent invoke`, tracing, and agent-as-MCP-tool exposure.

Conversely, AURA's value proposition is "orchestrate on top of your existing stack via
MCP." kagent already exposes every agent as an MCP tool through the controller `/mcp`
endpoint, so a single AURA orchestrator can fan out to an entire kagent fleet. Supporting
AURA first-class turns AURA from a sidecar product into a **peer in the kagent mesh**.

### Goals

- Run AURA-defined agents on kagent and have them behave like any other kagent agent
  (discoverable via A2A, invokable via `kagent invoke`, usable as a `type: Agent` tool by
  other agents, visible in the dashboard, traced through the shared OTel pipeline).
- Reuse existing kagent primitives — `ModelConfig` for the LLM/provider/secret, and
  `RemoteMCPServer`/`MCPServer` for tools — rather than re-specifying them in TOML.
- Keep secrets in Kubernetes `Secret`s, injected as env and referenced from TOML via
  `{{ env.VAR }}`; never render plaintext credentials into a ConfigMap.
- Provide a path that works **today** with zero controller changes, and a first-class
  runtime that delivers the ideal UX.
- Preserve access to AURA-only capabilities (orchestration workers, vector stores,
  `turn_depth`) without bloating the CRD.

### Non-Goals

- Reimplementing AURA's orchestration/RAG engine inside the kagent ADK runtimes.
- Translating AURA's OpenAI-compatible API into kagent — we standardize on AURA's **A2A**
  surface, which kagent already speaks end-to-end.
- Managing AURA's companion containers from the quickstart (LibreChat, Phoenix); kagent
  uses its own UI and OTel collector.
- Forking or vendoring AURA. We deploy the published `mezmo/aura` image as-is.

## Implementation Details

### Direction 1 — Run AURA agents on kagent

#### Phase 0 (today, no code): AURA as a BYO agent

Because AURA already serves A2A on `:8080` with the agent card at
`/.well-known/agent-card.json`, it satisfies kagent's BYO contract directly. An operator:

1. Puts the AURA TOML in a `ConfigMap`, with `{{ env.VAR }}` placeholders for secrets.
2. Puts the LLM API key in a `Secret`.
3. Creates an `Agent` of `type: BYO` that runs `mezmo/aura:1-latest`, mounts the ConfigMap
   at `CONFIG_PATH`, sets `AURA_ENABLE_A2A=true` and `AURA_SERVER_URL`, and injects the key.

This works now and is shipped as `examples/aura/`. It validates A2A compatibility and is a
fine on-ramp, but the UX is mediocre: TOML and kagent CRDs describe overlapping concepts
(model, MCP servers), the operator wires the API-key env by hand, and there is no fleet-level
consistency between an AURA agent's model config and the rest of the cluster.

#### Phase 1 (ideal UX): a first-class `aura` runtime — **implemented**

`aura` is now a value of the `DeclarativeRuntime` enum
(`go/api/v1alpha2/agent_types.go`), alongside `python` and `go`. Users write an
ordinary declarative `Agent` and the controller does the AURA-specific translation
(`go/core/internal/controller/translator/agent/aura.go`):

```yaml
apiVersion: kagent.dev/v1alpha2
kind: Agent
metadata:
  name: aura-sre
  namespace: kagent
spec:
  type: Declarative
  description: AURA SRE root-cause agent
  declarative:
    runtime: aura                     # <-- new
    modelConfig: default-model-config # reused: provider, model, secret
    systemMessage: |                  # -> [agent].system_prompt
      You are an SRE assistant. Investigate alerts and surface root cause with evidence.
    tools:                            # -> [mcp.servers.*]
      - type: McpServer
        mcpServer:
          name: mezmo-telemetry       # RemoteMCPServer/MCPServer ref
          apiGroup: kagent.dev
          kind: RemoteMCPServer
```

The agent translator gains an AURA path (sibling to the ADK path in
`go/core/internal/controller/translator/agent/`) that:

1. **Renders the TOML** from the CRD (`renderAuraConfigTOML`):
   - `declarative.systemMessage` / `systemMessageFrom` -> `[agent].system_prompt`
   - `declarative.modelConfig` -> `[agent.llm]` (`provider`, `model`, and `api_key`,
     `base_url`, or `region` as appropriate). The `ModelProvider` enum maps onto AURA's
     provider strings: **`OpenAI`->`openai`, `Anthropic`->`anthropic`, `Gemini`->`gemini`,
     `Bedrock`->`bedrock`, `Ollama`->`ollama`** (matching AURA's `examples/reference.toml`);
     unsupported providers (Azure, Vertex, SAP) return a clear validation error rather than
     emitting broken config. The per-provider env injection (API keys, AWS credentials +
     region, Ollama base URL) is reused from the existing ADK `translateModel`, so it stays
     consistent. OpenAI `base_url` is emitted when set (self-hosted / proxy / OpenRouter /
     mock endpoints); Bedrock emits `region = "{{ env.AWS_REGION }}"`.
   - each `tools[].mcpServer` -> a `[mcp.servers.<name>]` block with
     `transport` (`http_streamable`/`sse`), the resolved in-cluster URL, and `headers`.
     MCP resolution reuses the existing RemoteMCPServer/MCPServer/Service translation
     (proxy + egress handling included). Agent-to-agent tools are not yet supported on
     this runtime (validation error).
2. **Writes the rendered TOML to the agent config Secret** (`config.toml`, owned by the
   Agent) and mounts it read-only at `/etc/aura`; `CONFIG_PATH` points at the file.
3. **Deploys `mezmo/aura:1-latest`** (the default `DefaultAuraImage`, overridable via the
   `--aura-image` flag / `AURA_IMAGE` env / `controller.auraImage` Helm value) with
   `AURA_ENABLE_A2A=true`, `AURA_SERVER_URL` set to the in-cluster service URL, and
   `HOST`/`PORT`. Command is `./aura-web-server --verbose`.
4. **Injects the model secret** as an env var (the same Secret the ModelConfig already
   references), so the `{{ env.VAR }}` placeholder resolves without plaintext in the Secret
   payload. The ModelConfig's `Status.SecretHash` is folded into the pod config-hash so an
   in-place key rotation rolls the pod.
5. Reuses the existing deployment machinery (Service, readiness probe on
   `/.well-known/agent-card.json`, OTel env from `collectSharedEnv`, replicas, RBAC).

Net result: AURA "feels native." The same `Agent`, `ModelConfig`, and `RemoteMCPServer`
objects, the same dashboard, `kagent invoke`, tracing, and the same agent-as-MCP-tool
exposure apply uniformly. `validateRuntimeFeatures` emits a soft warning when an aura agent
configures kagent-ADK-only features (memory, context management, code execution, prompt
templates) that the runtime ignores.

**Escape hatch for AURA-only features — implemented.** AURA has knobs with no kagent
equivalent (`turn_depth`, `[orchestration]`, `[[vector_stores]]`). Rather than grow the CRD
to mirror AURA's whole schema, `spec.declarative.auraConfigFrom` references a ConfigMap or
Secret key whose raw TOML is **appended verbatim** after the generated config:

```yaml
    runtime: aura
    auraConfigFrom:
      type: ConfigMap
      name: aura-sre-overrides
      key: overrides.toml
```

The overlay is appended (not deep-merged) and is intended to add new top-level tables; it
cannot override keys already emitted in `[agent]`/`[agent.llm]`. This keeps the simple case
one-field-simple, gives power users AURA's full expressiveness without the CRD chasing
AURA's release cadence, and avoids a fragile partial TOML merge — matching the
OpenClaw-harness philosophy of generating config and letting the backend own the long tail.

### Direction 2 — kagent agents as AURA tools (bidirectional, works today)

AURA orchestrates over MCP. The kagent controller already exposes a Streamable HTTP `/mcp`
endpoint with `list_agents` and `invoke_agent`. Pointing an AURA config at it makes the
entire kagent fleet callable from an AURA orchestrator:

```toml
[mcp.servers.kagent]
transport = "http_streamable"
url = "http://kagent-controller.kagent.svc:8083/mcp"
headers = { "Authorization" = "Bearer {{ env.KAGENT_TOKEN }}" }
```

Combined with Direction 1, AURA agents and kagent agents call each other as peers: an AURA
SRE planner can invoke a declarative ADK Kubernetes-debugging agent, which can in turn call
back into AURA — all over A2A/MCP, all managed as kagent `Agent`s.

### How it scales

1. **One image, N agents.** Stock `mezmo/aura` image + one ConfigMap per agent. Adding an
   agent is one `Agent` CR — no per-agent image builds (the core BYO pain point disappears).
2. **Horizontal scale.** The AURA web server scales behind a Service via
   `deployment.replicas` (already in `SharedDeploymentSpec`); stateless chat turns spread
   across pods.
3. **Multi-tenancy.** Agents, `ModelConfig`, and tool servers are namespace-scoped with
   existing RBAC. Secrets stay as `Secret` refs and are injected as env, mirroring the
   Substrate/OpenClaw secret-ref pattern (no plaintext credentials in generated config).
4. **Fleet consistency.** Because AURA agents are just `Agent`s, they appear in
   `kagent get agent`, the dashboard, and A2A discovery, and are reusable as MCP tools by
   IDEs and other agents. Model/provider changes flow from a shared `ModelConfig`.
5. **Provider portability.** New providers added to `ModelConfig` flow into the AURA
   `[agent.llm]` mapping with no per-agent edits.
6. **Unified observability.** AURA already emits OTLP; the translator points
   `OTEL_EXPORTER_OTLP_ENDPOINT` at kagent's existing collector so AURA traces land in
   Jaeger next to ADK agents — no special-casing.
7. **Mesh fan-out.** A single AURA orchestrator reaches the whole fleet through `/mcp`,
   so scale-out of *capabilities* is additive: deploy more agents, the orchestrator sees them.

### Rollout

- **Phase 0 (done)** — `examples/aura/`: AURA-as-BYO, works today, no code.
- **Phase 1 (done)** — `runtime: aura` translator (`aura.go`) + `controller.auraImage`
  Helm value / `--aura-image` flag + OpenAI/Anthropic provider mapping + MCP-tool
  rendering + unit and golden tests + `examples/aura/aura-runtime-agent.yaml`.
- **Phase 2 (done)** — providers Gemini/Bedrock/Ollama + OpenAI `base_url`;
  `auraConfigFrom` raw-TOML overlay; `kagent init aura` scaffolding; opt-in E2E test.
- **Phase 3 (next, if demand warrants)** — typed first-class orchestration/RAG fields;
  Anthropic `base_url`; promote the E2E test to run-by-default once AURA A2A interop is
  confirmed in CI; list AURA agents in the public agent catalog (kagent.dev/agents,
  out-of-repo).

### Test Plan

- **Unit (Go) — done:** table-driven tests for the AURA TOML renderer and provider mapping
  (`aura_test.go`): mapping for OpenAI/Anthropic/Gemini/Bedrock/Ollama and rejection of
  unsupported providers, TOML escaping, key sanitization, and full config rendering
  (servers + sorted headers, base_url, Bedrock region, overlay ordering). CLI scaffolder
  tests (`frameworks/aura/generator_test.go`) cover provider canonicalization and the
  per-provider manifest shape.
- **Golden — done:** `agent_aura_runtime` (OpenAI + MCP tool), `agent_aura_bedrock`
  (Bedrock region + AWS-cred env), and `agent_aura_overlay` (auraConfigFrom append) exercise
  the full translation and are checked in CI alongside the existing golden suite.
- **E2E — done (opt-in):** `TestE2EInvokeAuraAgent` deploys a `runtime: aura` agent against
  the mock LLM (via the ModelConfig OpenAI `base_url`) and invokes it over A2A. It is gated
  behind `KAGENT_E2E_AURA=1` because it pulls the third-party `mezmo/aura` image and depends
  on AURA's A2A interop; promote to default once confirmed in CI.

## Alternatives

- **Adapter sidecar (A2A<->OpenAI).** Run AURA with only its OpenAI API and a sidecar that
  bridges to A2A. Rejected: unnecessary — AURA already speaks A2A, and a sidecar adds
  per-pod overhead and a second failure mode.
- **AgentHarness backend.** Model AURA as a new `AgentHarness` backend. Rejected: AgentHarness
  targets exec/SSH sandbox VMs with no agent runtime; AURA *is* an agent runtime that serves
  A2A, so it fits the Agent/BYO model far more naturally.
- **Dedicated `spec.aura` block (not under Declarative).** A fully typed AURA spec. Rejected
  as the default: it duplicates `modelConfig`/`tools` and forces the CRD to track AURA's
  schema. The `runtime: aura` + overlay approach reuses existing fields and degrades
  gracefully.
- **BYO-only (stop at Phase 0).** Viable but leaves the overlapping-config and
  manual-secret-wiring UX problems unsolved and does not scale cleanly to large fleets.

## Open Questions

- Image distribution: pin a digest in a kagent Helm value, mirror to `ghcr.io/kagent-dev`,
  or require users to set the image? (Leaning: default Helm value, overridable.)
- AURA A2A maturity vs. kagent's A2A client expectations (auth header propagation, streaming
  semantics, task lifecycle) — needs an interop test pass; document any required AURA flags.
- Should `[orchestration]` worker LLMs be expressible via multiple `ModelConfig` refs, or is
  the raw-TOML overlay sufficient for v1? (Leaning: overlay for v1.)
- Session/memory: AURA's state model vs. kagent's `Memory`/`Context` features — keep them
  independent initially.
