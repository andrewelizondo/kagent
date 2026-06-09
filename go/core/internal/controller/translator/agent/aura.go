package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kagent-dev/kagent/go/api/v1alpha2"
	"github.com/kagent-dev/kagent/go/core/pkg/env"
	"github.com/kagent-dev/kmcp/api/v1alpha1"
)

// DefaultAuraImage is the container image used for agents whose declarative
// runtime is "aura". It can be overridden with the --aura-image controller flag
// (AURA_IMAGE env / controller.auraImage Helm value).
var DefaultAuraImage = "mezmo/aura:1-latest"

const (
	// auraConfigDir is where the generated AURA config Secret is mounted.
	auraConfigDir = "/etc/aura"
	// auraConfigFile is the TOML file name within auraConfigDir.
	auraConfigFile = "config.toml"
	// auraConfigSecretKey is the key under which the rendered TOML is stored in
	// the agent config Secret.
	auraConfigSecretKey = "config.toml"
	// auraPort is the port AURA binds to and that kagent exposes over A2A.
	auraPort = int32(8080)
)

// auraMCPServer is a resolved MCP tool server rendered into AURA's
// [mcp.servers.<name>] TOML table.
type auraMCPServer struct {
	Name      string
	Transport string
	URL       string
	Headers   map[string]string
}

// auraConfigInput holds everything needed to render an AURA TOML configuration.
type auraConfigInput struct {
	Name         string
	SystemPrompt string
	Provider     string
	// APIKeyEnv, when non-empty, renders api_key = "{{ env.<APIKeyEnv> }}".
	APIKeyEnv string
	Model     string
	// BaseURL, when non-empty, renders base_url (custom OpenAI-compatible endpoint).
	BaseURL string
	// Region, when non-empty, renders a region key (Bedrock).
	Region  string
	Servers []auraMCPServer
	// Overlay is optional raw TOML appended verbatim after the generated config.
	Overlay string
}

// auraProviderConfig maps a kagent ModelProvider to AURA's provider string and
// the environment variable AURA expects the API key under (empty when the
// provider does not use a single API key, e.g. Bedrock/Ollama). The boolean
// reports whether the provider is supported by the aura runtime.
//
// AURA supports openai, anthropic, gemini, bedrock, and ollama (see
// github.com/mezmo/aura examples/reference.toml).
func auraProviderConfig(provider v1alpha2.ModelProvider) (auraProvider, apiKeyEnv string, ok bool) {
	switch provider {
	case v1alpha2.ModelProviderOpenAI:
		return "openai", env.OpenAIAPIKey.Name(), true
	case v1alpha2.ModelProviderAnthropic:
		return "anthropic", env.AnthropicAPIKey.Name(), true
	case v1alpha2.ModelProviderGemini:
		return "gemini", env.GoogleAPIKey.Name(), true
	case v1alpha2.ModelProviderBedrock:
		// Bedrock authenticates via AWS credentials/region (injected as env by
		// translateModel), not a single api_key.
		return "bedrock", "", true
	case v1alpha2.ModelProviderOllama:
		// Ollama uses no API key; the server URL is injected as env by translateModel.
		return "ollama", "", true
	default:
		return "", "", false
	}
}

// compileAuraAgent translates a declarative Agent with runtime "aura" into the
// rendered TOML config, a resolved AURA deployment, and the secret-hash bytes
// that drive a rollout when a referenced Secret rotates.
func (a *adkApiTranslator) compileAuraAgent(ctx context.Context, agent v1alpha2.AgentObject) (string, *resolvedDeployment, []byte, error) {
	spec := agent.GetAgentSpec()
	decl := spec.Declarative
	if decl == nil {
		return "", nil, nil, fmt.Errorf("declarative spec is required for aura runtime")
	}

	model, err := a.getAuraModelConfig(ctx, agent.GetNamespace(), decl.ModelConfig)
	if err != nil {
		return "", nil, nil, err
	}

	auraProvider, apiKeyEnv, ok := auraProviderConfig(model.Spec.Provider)
	if !ok {
		return "", nil, nil, NewValidationError(
			"aura runtime does not support model provider %q (supported: OpenAI, Anthropic, Gemini, Bedrock, Ollama)",
			model.Spec.Provider,
		)
	}
	if model.Spec.Model == "" {
		return "", nil, nil, NewValidationError("model config %q does not specify a model", model.Name)
	}

	systemPrompt, err := a.resolveRawSystemMessage(ctx, agent)
	if err != nil {
		return "", nil, nil, err
	}

	servers, secretHashBytes, err := a.resolveAuraMCPServers(ctx, agent)
	if err != nil {
		return "", nil, nil, err
	}

	// Reuse the ADK model translation purely for its deployment data: the
	// provider-specific env injection (API keys, AWS credentials + region,
	// Ollama base URL, ...) and the Secret hash that drives a rollout on
	// rotation. The returned adk.Model is not used by AURA.
	_, mdd, modelHash, err := a.translateModel(ctx, model.Namespace, model.Name)
	if err != nil {
		return "", nil, nil, err
	}
	secretHashBytes = append(secretHashBytes, modelHash...)

	// Bedrock renders a region (sourced from the AWS_REGION env that
	// translateModel injects) instead of an api_key.
	region := ""
	if model.Spec.Provider == v1alpha2.ModelProviderBedrock {
		region = "{{ env." + env.AWSRegion.Name() + " }}"
	}

	// Custom OpenAI-compatible endpoint (self-hosted, proxy, OpenRouter, ...).
	baseURL := ""
	if model.Spec.Provider == v1alpha2.ModelProviderOpenAI && model.Spec.OpenAI != nil {
		baseURL = model.Spec.OpenAI.BaseURL
	}

	// Optional raw-TOML overlay for AURA-only features (turn_depth,
	// [orchestration], [[vector_stores]], extra [mcp.servers.*]).
	overlay := ""
	if decl.AuraConfigFrom != nil {
		overlay, err = decl.AuraConfigFrom.Resolve(ctx, a.kube, agent.GetNamespace())
		if err != nil {
			return "", nil, nil, fmt.Errorf("failed to resolve auraConfigFrom: %w", err)
		}
	}

	configTOML := renderAuraConfigTOML(auraConfigInput{
		Name:         agent.GetName(),
		SystemPrompt: systemPrompt,
		Provider:     auraProvider,
		APIKeyEnv:    apiKeyEnv,
		Model:        model.Spec.Model,
		BaseURL:      baseURL,
		Region:       region,
		Servers:      servers,
		Overlay:      overlay,
	})

	dep, err := resolveAuraDeployment(agent, mdd.EnvVars, mdd.Volumes, mdd.VolumeMounts)
	if err != nil {
		return "", nil, nil, err
	}

	return configTOML, dep, secretHashBytes, nil
}

// getAuraModelConfig resolves the ModelConfig for an aura agent, falling back to
// the controller default when the agent does not name one.
func (a *adkApiTranslator) getAuraModelConfig(ctx context.Context, namespace, modelConfigName string) (*v1alpha2.ModelConfig, error) {
	ref := a.defaultModelConfig
	if modelConfigName != "" {
		ref.Name = modelConfigName
		ref.Namespace = namespace
	}
	if ref.Namespace == "" {
		ref.Namespace = namespace
	}

	model := &v1alpha2.ModelConfig{}
	if err := a.kube.Get(ctx, ref, model); err != nil {
		return nil, fmt.Errorf("failed to get model config %q for aura agent: %w", ref.Name, err)
	}
	return model, nil
}

// resolveAuraMCPServers resolves the agent's MCP tools into AURA mcp.servers
// entries, returning the combined secret-hash bytes for the referenced servers.
func (a *adkApiTranslator) resolveAuraMCPServers(ctx context.Context, agent v1alpha2.AgentObject) ([]auraMCPServer, []byte, error) {
	decl := agent.GetAgentSpec().Declarative
	var servers []auraMCPServer
	var secretHashBytes []byte
	seen := map[string]bool{}

	for _, tool := range decl.Tools {
		switch {
		case tool.McpServer != nil:
			server, hash, err := a.resolveAuraMCPServer(ctx, agent.GetNamespace(), tool)
			if err != nil {
				return nil, nil, err
			}
			// Ensure unique TOML table keys when two tools sanitize to the same name.
			base := server.Name
			for i := 2; seen[server.Name]; i++ {
				server.Name = fmt.Sprintf("%s_%d", base, i)
			}
			seen[server.Name] = true
			servers = append(servers, server)
			secretHashBytes = append(secretHashBytes, hash...)
		case tool.Agent != nil:
			return nil, nil, NewValidationError("aura runtime does not support agent tools; use MCP tools instead")
		default:
			return nil, nil, fmt.Errorf("tool must have a provider or tool server")
		}
	}
	return servers, secretHashBytes, nil
}

// resolveAuraMCPServer resolves a single MCP tool into an auraMCPServer by
// reusing the existing RemoteMCPServer connection translation (URL, headers,
// proxy/egress handling).
func (a *adkApiTranslator) resolveAuraMCPServer(ctx context.Context, agentNamespace string, tool *v1alpha2.Tool) (auraMCPServer, []byte, error) {
	agentHeaders, err := tool.ResolveHeaders(ctx, a.kube, agentNamespace)
	if err != nil {
		return auraMCPServer{}, nil, err
	}

	rms, proxyURL, egressRewrite, err := a.auraResolveRemoteMCPServer(ctx, agentNamespace, tool.McpServer)
	if err != nil {
		return auraMCPServer{}, nil, err
	}

	name := sanitizeTOMLKey(tool.McpServer.Name)
	switch rms.Spec.Protocol {
	case v1alpha2.RemoteMCPServerProtocolSse:
		params, err := a.translateSseHttpTool(ctx, rms, agentHeaders, proxyURL, egressRewrite)
		if err != nil {
			return auraMCPServer{}, nil, err
		}
		return auraMCPServer{Name: name, Transport: "sse", URL: params.Url, Headers: params.Headers}, remoteMCPServerSecretHashBytes(rms), nil
	default:
		params, err := a.translateStreamableHttpTool(ctx, rms, agentHeaders, proxyURL, egressRewrite)
		if err != nil {
			return auraMCPServer{}, nil, err
		}
		return auraMCPServer{Name: name, Transport: "http_streamable", URL: params.Url, Headers: params.Headers}, remoteMCPServerSecretHashBytes(rms), nil
	}
}

// auraResolveRemoteMCPServer resolves an McpServerTool reference (RemoteMCPServer,
// in-cluster MCPServer, or Service) into a RemoteMCPServer plus the proxy/egress
// decision, mirroring translateMCPServerTarget without mutating an adk config.
func (a *adkApiTranslator) auraResolveRemoteMCPServer(ctx context.Context, agentNamespace string, toolServer *v1alpha2.McpServerTool) (*v1alpha2.RemoteMCPServer, string, bool, error) {
	ref := toolServer.NamespacedName(agentNamespace)
	switch toolServer.GroupKind() {
	case schema.GroupKind{Group: "", Kind: ""},
		schema.GroupKind{Group: "", Kind: "MCPServer"},
		schema.GroupKind{Group: "kagent.dev", Kind: "MCPServer"}:
		mcpServer := &v1alpha1.MCPServer{}
		if err := a.kube.Get(ctx, ref, mcpServer); err != nil {
			return nil, "", false, err
		}
		rms, err := ConvertMCPServerToRemoteMCPServer(mcpServer)
		if err != nil {
			return nil, "", false, err
		}
		return rms, "", false, nil

	case schema.GroupKind{Group: "", Kind: "RemoteMCPServer"},
		schema.GroupKind{Group: "kagent.dev", Kind: "RemoteMCPServer"}:
		rms := &v1alpha2.RemoteMCPServer{}
		if err := a.kube.Get(ctx, ref, rms); err != nil {
			return nil, "", false, err
		}
		proxyURL := ""
		egressRewrite := false
		if a.globalProxyURL != "" && a.isInternalK8sURL(ctx, rms.Spec.URL, agentNamespace) {
			proxyURL = a.globalProxyURL
		} else if a.mcpEgressPlaintext {
			egressRewrite = true
		}
		return rms, proxyURL, egressRewrite, nil

	case schema.GroupKind{Group: "", Kind: "Service"},
		schema.GroupKind{Group: "core", Kind: "Service"}:
		svc := &corev1.Service{}
		if err := a.kube.Get(ctx, ref, svc); err != nil {
			return nil, "", false, err
		}
		rms, err := ConvertServiceToRemoteMCPServer(svc)
		if err != nil {
			return nil, "", false, err
		}
		return rms, "", false, nil

	default:
		return nil, "", false, fmt.Errorf("unknown tool server type: %s", toolServer.GroupKind())
	}
}

// resolveAuraDeployment builds the resolvedDeployment for an aura agent: the
// AURA image, the A2A/config environment, and the shared deployment knobs from
// the declarative deployment spec. The config volume itself is mounted by
// buildConfigSecret.
func resolveAuraDeployment(agent v1alpha2.AgentObject, modelEnv []corev1.EnvVar, modelVolumes []corev1.Volume, modelVolumeMounts []corev1.VolumeMount) (*resolvedDeployment, error) {
	spec := agent.GetAgentSpec()

	deploySpec := v1alpha2.DeclarativeDeploymentSpec{}
	if spec.Declarative.Deployment != nil {
		deploySpec = *spec.Declarative.Deployment
	}

	if err := validateExtraContainers(deploySpec.ExtraContainers); err != nil {
		return nil, err
	}

	host := DefaultAgentBindHost
	if host == "" {
		host = "0.0.0.0"
	}

	// AURA environment. User-provided env (deploySpec.Env) is appended last so
	// operators can override these defaults.
	auraEnv := []corev1.EnvVar{
		{Name: "HOST", Value: host},
		{Name: "PORT", Value: fmt.Sprintf("%d", auraPort)},
		{Name: "AURA_ENABLE_A2A", Value: "true"},
		{Name: "AURA_SERVER_URL", Value: fmt.Sprintf("http://%s.%s.svc:%d", agent.GetName(), agent.GetNamespace(), auraPort)},
		{Name: "CONFIG_PATH", Value: auraConfigDir + "/" + auraConfigFile},
	}
	envVars := append(auraEnv, modelEnv...)
	envVars = append(envVars, deploySpec.Env...)

	// Model-derived volumes (e.g. a pinned TLS CA bundle) plus any user volumes.
	volumes := append(slices.Clone(deploySpec.Volumes), modelVolumes...)
	volumeMounts := append(slices.Clone(deploySpec.VolumeMounts), modelVolumeMounts...)

	imagePullPolicy := corev1.PullPolicy(DefaultImageConfig.PullPolicy)
	if deploySpec.ImagePullPolicy != "" {
		imagePullPolicy = corev1.PullPolicy(deploySpec.ImagePullPolicy)
	}

	dep := &resolvedDeployment{
		Image:                DefaultAuraImage,
		Cmd:                  "./aura-web-server",
		Args:                 []string{"--verbose"},
		Port:                 auraPort,
		ImagePullPolicy:      imagePullPolicy,
		Replicas:             deploySpec.Replicas,
		ImagePullSecrets:     slices.Clone(deploySpec.ImagePullSecrets),
		Volumes:              volumes,
		VolumeMounts:         volumeMounts,
		Labels:               getDefaultLabels(agent.GetName(), deploySpec.Labels),
		Annotations:          deploySpec.Annotations,
		Env:                  envVars,
		Resources:            getDefaultResources(deploySpec.Resources),
		Tolerations:          slices.Clone(deploySpec.Tolerations),
		Affinity:             deploySpec.Affinity,
		NodeSelector:         deploySpec.NodeSelector,
		SecurityContext:      deploySpec.SecurityContext,
		PodSecurityContext:   deploySpec.PodSecurityContext,
		ServiceAccountName:   deploySpec.ServiceAccountName,
		ServiceAccountConfig: deploySpec.ServiceAccountConfig,
		ExtraContainers:      slices.Clone(deploySpec.ExtraContainers),
	}

	// Precedence: agent-level serviceAccountName > global default > auto-created SA (agent name).
	if dep.ServiceAccountName == nil {
		if DefaultServiceAccountName != "" {
			dep.ServiceAccountName = new(DefaultServiceAccountName)
		} else {
			dep.ServiceAccountName = new(agent.GetName())
		}
	}

	return dep, nil
}

// renderAuraConfigTOML renders an AURA TOML configuration from the resolved
// inputs. All string values are escaped as TOML basic strings.
func renderAuraConfigTOML(in auraConfigInput) string {
	var b strings.Builder

	b.WriteString("# Generated by kagent for the \"aura\" runtime. Do not edit; managed by the Agent CR.\n\n")

	b.WriteString("[agent]\n")
	fmt.Fprintf(&b, "name = %s\n", tomlString(in.Name))
	fmt.Fprintf(&b, "system_prompt = %s\n", tomlString(in.SystemPrompt))
	b.WriteString("\n")

	b.WriteString("[agent.llm]\n")
	fmt.Fprintf(&b, "provider = %s\n", tomlString(in.Provider))
	if in.APIKeyEnv != "" {
		fmt.Fprintf(&b, "api_key = %s\n", tomlString(fmt.Sprintf("{{ env.%s }}", in.APIKeyEnv)))
	}
	fmt.Fprintf(&b, "model = %s\n", tomlString(in.Model))
	if in.BaseURL != "" {
		fmt.Fprintf(&b, "base_url = %s\n", tomlString(in.BaseURL))
	}
	if in.Region != "" {
		fmt.Fprintf(&b, "region = %s\n", tomlString(in.Region))
	}

	for _, s := range in.Servers {
		b.WriteString("\n")
		fmt.Fprintf(&b, "[mcp.servers.%s]\n", s.Name)
		fmt.Fprintf(&b, "transport = %s\n", tomlString(s.Transport))
		fmt.Fprintf(&b, "url = %s\n", tomlString(s.URL))
		if len(s.Headers) > 0 {
			keys := make([]string, 0, len(s.Headers))
			for k := range s.Headers {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			pairs := make([]string, 0, len(keys))
			for _, k := range keys {
				pairs = append(pairs, fmt.Sprintf("%s = %s", tomlString(k), tomlString(s.Headers[k])))
			}
			fmt.Fprintf(&b, "headers = { %s }\n", strings.Join(pairs, ", "))
		}
	}

	// Append the optional raw-TOML overlay verbatim. It is intended to add new
	// top-level tables (e.g. [orchestration], [[vector_stores]]); it cannot
	// override keys already emitted in [agent]/[agent.llm].
	if overlay := strings.TrimSpace(in.Overlay); overlay != "" {
		b.WriteString("\n# --- overlay from spec.declarative.auraConfigFrom ---\n")
		b.WriteString(overlay)
		b.WriteString("\n")
	}

	return b.String()
}

// tomlString renders s as a TOML basic string (always single-line, fully escaped).
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// sanitizeTOMLKey converts an arbitrary name into a TOML bare key
// ([A-Za-z0-9_-]). Invalid characters become underscores.
func sanitizeTOMLKey(name string) string {
	if name == "" {
		return "server"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
