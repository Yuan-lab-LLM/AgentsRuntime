package hermes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/llmconfig"
	"github.com/iamlovingit/clawmanager-agent/managedconfig"
	"gopkg.in/yaml.v3"
)

const (
	desktopWebHermesVersion = "0.21.0"
	desktopWebHermesGitRef  = "v2026.8.31"
	desktopWebHermesCommit  = "29112bef099274229cadff79cdff7bf7b99c4b77"
	desktopWebMarkerFile    = ".clawmanager-hermes-workspace.json"
)

type desktopWebWorkspaceVersion struct {
	SchemaVersion int    `json:"schema_version"`
	HermesVersion string `json:"hermes_version"`
	HermesGitRef  string `json:"hermes_git_ref"`
	HermesCommit  string `json:"hermes_commit"`
	BackendMode   string `json:"backend_mode"`
}

func desktopWebLLMConfig(cfg gateway.Config, req gateway.CreateGatewayRequest) (gateway.Config, error) {
	// The common resolver permits deployment defaults. Instance credentials and
	// endpoints deliberately have no such fallback in the shared Lite runtime.
	cfg.LLMAPIKey, cfg.LLMBaseURL, cfg.LLMAPIKeyEnvName = "", "", ""
	cfg.LLMAPIKeySet = false
	settings, err := llmconfig.ResolveGateway(cfg, req, llmconfig.ResolveOptions{})
	if err != nil {
		return gateway.Config{}, fmt.Errorf("missing_llm_credentials: invalid Hermes instance LLM configuration")
	}
	if !settings.APIKeySet || strings.TrimSpace(settings.APIKey) == "" {
		return gateway.Config{}, fmt.Errorf("missing_llm_credentials: missing Hermes instance LLM token")
	}
	if strings.ContainsAny(settings.APIKey, "\r\n\x00") {
		return gateway.Config{}, fmt.Errorf("missing_llm_credentials: invalid Hermes instance LLM token")
	}
	if !validDesktopWebURL(settings.BaseURL, false) {
		return gateway.Config{}, fmt.Errorf("missing_llm_credentials: missing or invalid Hermes instance LLM base URL")
	}
	endpoint, _ := url.Parse(settings.BaseURL)
	rawLocal, _ := requestEnvValue(req, "CLAWMANAGER_LLM_BASE_URL")
	trustedLocal := rawLocal == managedconfig.ModelURL && cfg.NetworkSecurityProfile == "isolated-network-v1" && req.SecurityProfile == cfg.NetworkSecurityProfile && settings.BaseURL == managedconfig.ModelURL
	if !trustedLocal && !(strings.HasSuffix(endpoint.Hostname(), ".svc.cluster.local") || strings.HasSuffix(endpoint.Hostname(), ".svc")) {
		return gateway.Config{}, fmt.Errorf("missing_llm_credentials: Hermes LLM URL must use Kubernetes Service DNS")
	}
	return settings.ApplyTo(cfg), nil
}

func validDesktopWebURL(raw string, originOnly bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(raw, "\r\n\x00*\\") {
		return false
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return false
		}
	}
	return !originOnly || u.Path == "" || u.Path == "/"
}

// desktopWebProxyConfig resolves only explicitly supplied deployment/request
// configuration. In particular it does not infer trust from POD_IP.
func desktopWebProxyConfig(cfg gateway.Config, req gateway.CreateGatewayRequest) (string, []string, error) {
	origin := cfg.PublicOrigin
	if value, ok := requestEnvValue(req, "CLAWMANAGER_CONTROL_UI_ORIGIN"); ok {
		origin = value
	}
	origin = strings.TrimSpace(origin)
	if !validDesktopWebURL(origin, true) {
		return "", nil, fmt.Errorf("invalid_control_ui_origin: missing or invalid Hermes control UI origin")
	}
	origin = strings.TrimSuffix(origin, "/")
	if cfg.ProxyPolicyFile != "" {
		// Managed peers are ephemeral lease state, never request-supplied or
		// persisted in the instance config. The boundary reads the Agent file.
		return origin, []string{}, nil
	}
	proxies := append([]string{}, cfg.TrustedProxies...)
	if value, ok := requestEnvValue(req, "CLAWMANAGER_TRUSTED_PROXY_CIDRS"); ok {
		proxies = strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
	}
	if len(proxies) == 0 {
		return "", nil, fmt.Errorf("invalid_trusted_proxies: explicit proxy addresses are required")
	}
	for index, proxy := range proxies {
		proxy = strings.TrimSpace(proxy)
		if ip := net.ParseIP(proxy); ip != nil {
			proxies[index] = ip.String()
			continue
		}
		_, network, err := net.ParseCIDR(proxy)
		if err != nil {
			return "", nil, fmt.Errorf("invalid_trusted_proxies: invalid Hermes trusted proxy address")
		}
		ones, _ := network.Mask.Size()
		if ones == 0 {
			return "", nil, fmt.Errorf("invalid_trusted_proxies: Hermes trusted proxies must not include an unbounded network")
		}
		proxies[index] = network.String()
	}
	return origin, proxies, nil
}

func writeDesktopWebConfig(cfg gateway.Config, req gateway.CreateGatewayRequest, workspace string) error {
	resolved, err := desktopWebLLMConfig(cfg, req)
	if err != nil {
		return err
	}
	username, password, err := desktopWebCredentials(cfg, req)
	if err != nil {
		return err
	}
	origin, proxies, err := desktopWebProxyConfig(cfg, req)
	if err != nil {
		return err
	}
	root, err := openDesktopWebHome(cfg, req, workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	version := desktopWebWorkspaceVersion{1, desktopWebHermesVersion, desktopWebHermesGitRef, desktopWebHermesCommit, "dashboard"}
	marker, err := readDesktopWebFile(root, desktopWebMarkerFile)
	if err != nil {
		return err
	}
	if marker != nil {
		var previous desktopWebWorkspaceVersion
		if json.Unmarshal(marker, &previous) != nil || previous != version {
			return fmt.Errorf("incompatible_workspace_version: Hermes workspace version is incompatible; use its compatible image or an explicit backed-up migration")
		}
	}
	files := []string{"config.yaml", ".env", "gateway.json"}
	existing := make(map[string][]byte, len(files))
	for _, name := range files {
		data, err := readDesktopWebFile(root, name)
		if err != nil {
			return err
		}
		existing[name] = data
	}
	basePath := "/api/v1/instances/" + strconv.Itoa(req.InstanceID) + "/proxy"
	configData, err := mergeDesktopWebYAML(existing["config.yaml"], resolved, origin+basePath, proxies)
	if err != nil {
		return err
	}
	envData, err := mergeDesktopWebEnv(existing[".env"], map[string]string{
		"CLAWMANAGER_LLM_API_KEY":                   resolved.LLMAPIKey,
		"CLAWMANAGER_LLM_BASE_URL":                  resolved.LLMBaseURL,
		"OPENAI_API_KEY":                            resolved.LLMAPIKey,
		"OPENAI_BASE_URL":                           resolved.LLMBaseURL,
		"OPENAI_API_BASE":                           resolved.LLMBaseURL,
		"CUSTOM_BASE_URL":                           resolved.LLMBaseURL,
		"HERMES_DASHBOARD_BASIC_AUTH_USERNAME":      username,
		"HERMES_DASHBOARD_BASIC_AUTH_PASSWORD":      password,
		"HERMES_DASHBOARD_BASIC_AUTH_SECRET":        dashboardSigningSecret(password),
		"HERMES_DASHBOARD_BASIC_AUTH_PASSWORD_HASH": "",
		"HERMES_DASHBOARD_PUBLIC_URL":               origin + basePath,
	})
	if err != nil {
		return err
	}
	gatewayData, err := mergeDesktopWebGatewayJSON(existing["gateway.json"], basePath, origin, proxies)
	if err != nil {
		return err
	}
	// Validate every document before changing any existing file. Preserve a
	// single immutable pre-migration copy; retries never replace the backup.
	if marker == nil {
		for _, name := range files {
			if existing[name] == nil {
				continue
			}
			backup := name + ".clawmanager-pre-desktop-web.bak"
			data, err := readDesktopWebFile(root, backup)
			if err != nil {
				return err
			}
			if data == nil {
				if err := writeDesktopWebFile(root, backup, existing[name], req.UID, req.GID); err != nil {
					return err
				}
			}
		}
	}
	contents := [][]byte{configData, envData, gatewayData}
	for index, name := range files {
		if err := writeDesktopWebFile(root, name, contents[index], req.UID, req.GID); err != nil {
			return err
		}
	}
	data, _ := json.MarshalIndent(version, "", "  ")
	return writeDesktopWebFile(root, desktopWebMarkerFile, append(data, '\n'), req.UID, req.GID)
}

// Platform-owned YAML leaves: model routing/credentials, the same leaves on
// managed providers, dashboard.public_url/trusted_proxies, and stale Dashboard
// password/password_hash leaves. The bundled Basic auth provider is removed
// from plugins.disabled; other plugin configuration and user data are preserved.
func mergeDesktopWebYAML(existing []byte, cfg gateway.Config, publicURL string, proxies []string) ([]byte, error) {
	document, err := parseExistingDesktopWebYAML(existing)
	if err != nil {
		return nil, err
	}
	refs := llmconfig.FromGatewayConfig(cfg).ModelRefs("clawmanager")
	if len(refs) == 0 {
		refs = []llmconfig.ModelRef{{ProviderID: "clawmanager", ModelID: "auto"}}
	}
	if cfg.LLMModelsQualified {
		for index := range refs {
			// Pinned upstream reserves auto and builtin vendor IDs before its
			// custom-runtime resolver. A managed namespace always selects the
			// configured OpenAI-compatible endpoint and its instance key_env.
			refs[index].ProviderID = "clawmanager-" + strings.ToLower(refs[index].ProviderID)
		}
	}
	providers := map[string]any{}
	for _, group := range llmconfig.GroupModelRefs(refs) {
		models := map[string]any{}
		for _, model := range group.ModelIDs {
			models[model] = map[string]any{}
		}
		providers[group.ProviderID] = map[string]any{
			"name": group.ProviderID, "base_url": cfg.LLMBaseURL,
			"enabled": true, "api": "", "url": "", "key_cmd": "",
			"default_model": group.ModelIDs[0], "transport": "openai_chat",
			"api_mode": "chat_completions", "key_env": "OPENAI_API_KEY",
			"api_key": "", "api_key_env": "OPENAI_API_KEY", "models": models,
		}
	}
	managed := map[string]any{
		"model":     map[string]any{"default": refs[0].ModelID, "provider": refs[0].ProviderID, "base_url": cfg.LLMBaseURL, "key_env": "OPENAI_API_KEY", "api_key": ""},
		"providers": providers,
		"dashboard": map[string]any{
			"public_url": publicURL, "trusted_proxies": proxies,
			// The instance credentials are injected through restricted .env and
			// the child's environment. Retire old at-rest login credentials while
			// retaining unrelated basic_auth metadata, such as its signing key.
			"basic_auth": map[string]any{"password": "", "password_hash": ""},
		},
	}
	var overlay yaml.Node
	if err := overlay.Encode(managed); err != nil {
		return nil, fmt.Errorf("workspace_unavailable: encode managed Hermes YAML")
	}
	mergeDesktopWebYAMLNode(document.Content[0], &overlay)
	if err := applyManagedVisionYAML(document.Content[0], cfg); err != nil {
		return nil, err
	}
	enableDesktopWebBasicAuth(document.Content[0])
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("workspace_unavailable: encode Hermes config.yaml")
	}
	return buffer.Bytes(), nil
}

// Mirrors pinned upstream ensure_basic_auth_plugin_enabled_in_config. Basic
// is a bundled backend and auto-loads unless either its canonical key or its
// legacy name is denied. Do not enable other plugins or replace their lists.
func enableDesktopWebBasicAuth(mapping *yaml.Node) {
	plugins := desktopWebYAMLMappingValue(mapping, "plugins")
	if plugins == nil {
		return
	}
	detachDesktopWebYAMLAlias(plugins)
	disabled := desktopWebYAMLMappingValue(plugins, "disabled")
	if disabled == nil {
		return
	}
	detachDesktopWebYAMLAlias(disabled)
	if disabled.Kind != yaml.SequenceNode {
		return
	}
	var retained []*yaml.Node
	for _, entry := range disabled.Content {
		value := entry
		for value.Kind == yaml.AliasNode && value.Alias != nil {
			value = value.Alias
		}
		if value.Kind == yaml.ScalarNode && value.Tag == "!!str" &&
			(value.Value == "basic" || value.Value == "dashboard_auth/basic") {
			continue
		}
		retained = append(retained, entry)
	}
	disabled.Content = retained
}

func desktopWebYAMLMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

// A user may reuse an anchored plugin list in unrelated settings. Editing its
// alias must not also edit the anchor's original value elsewhere in the YAML.
func detachDesktopWebYAMLAlias(node *yaml.Node) {
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		*node = *cloneDesktopWebYAMLNode(node.Alias)
	}
}

func cloneDesktopWebYAMLNode(node *yaml.Node) *yaml.Node {
	copy := *node
	copy.Anchor = ""
	copy.Content = make([]*yaml.Node, len(node.Content))
	for index, child := range node.Content {
		copy.Content[index] = cloneDesktopWebYAMLNode(child)
	}
	return &copy
}

// The previous writer appended a second model/providers mapping in a marked
// block. Parse those sections separately so migration removes duplicate keys
// without discarding unknown fields the user added inside that block.
func parseExistingDesktopWebYAML(existing []byte) (*yaml.Node, error) {
	content := string(existing)
	start := strings.Index(content, managedConfigStart)
	if start < 0 {
		return parseDesktopWebYAML(existing)
	}
	end := strings.Index(content[start:], managedConfigEnd)
	if end < 0 {
		return nil, fmt.Errorf("workspace_unavailable: incomplete managed Hermes YAML block; existing file preserved")
	}
	end += start
	base, err := parseDesktopWebYAML([]byte(content[:start] + content[end+len(managedConfigEnd):]))
	if err != nil {
		return nil, err
	}
	managed, err := parseDesktopWebYAML([]byte(content[start+len(managedConfigStart) : end]))
	if err != nil {
		return nil, err
	}
	mergeDesktopWebYAMLNode(base.Content[0], managed.Content[0])
	return base, nil
}

func parseDesktopWebYAML(data []byte) (*yaml.Node, error) {
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil && err != io.EOF {
		// Parser errors can quote secret values; report only the file/category.
		return nil, fmt.Errorf("workspace_unavailable: invalid Hermes config.yaml; existing file preserved")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("workspace_unavailable: Hermes config.yaml must contain one YAML document")
	}
	if len(document.Content) == 0 {
		document.Kind = yaml.DocumentNode
		document.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	if document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("workspace_unavailable: Hermes config.yaml must be a mapping")
	}
	var decoded map[string]any
	if err := document.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("workspace_unavailable: invalid or duplicate Hermes YAML fields; existing file preserved")
	}
	remaining := 200000
	return materializeDesktopWebYAML(&document, 0, &remaining)
}

// Expand aliases before touching managed mappings. Otherwise modifying an
// anchored provider can change an unrelated user field through its alias, and
// overlaying an alias can silently discard the user's inherited settings.
func materializeDesktopWebYAML(node *yaml.Node, depth int, remaining *int) (*yaml.Node, error) {
	*remaining--
	if depth > 128 || *remaining < 0 {
		return nil, fmt.Errorf("workspace_unavailable: Hermes YAML aliases exceed the supported size")
	}
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		copy, err := materializeDesktopWebYAML(node.Alias, depth+1, remaining)
		if err == nil && node.HeadComment != "" {
			copy.HeadComment = node.HeadComment
		}
		return copy, err
	}
	copy := *node
	copy.Anchor, copy.Alias = "", nil
	copy.Content = make([]*yaml.Node, len(node.Content))
	for index, child := range node.Content {
		var err error
		copy.Content[index], err = materializeDesktopWebYAML(child, depth+1, remaining)
		if err != nil {
			return nil, err
		}
	}
	if copy.Kind == yaml.MappingNode {
		if err := flattenDesktopWebYAMLMerge(&copy); err != nil {
			return nil, err
		}
	}
	return &copy, nil
}

// YAML merge keys are shallow: direct entries beat every inherited entry,
// and an earlier map in a merge sequence beats later maps. Resolve them before
// the platform's recursive overlay so inherited provider metadata stays visible.
// Children have already been copied under materializeDesktopWebYAML's bounds;
// flattening only reuses those nodes and never follows aliases or recurses.
func flattenDesktopWebYAMLMerge(mapping *yaml.Node) error {
	keyID := func(key *yaml.Node) string { return key.Tag + "\x00" + key.Value }
	isMerge := func(key *yaml.Node) bool { return key.Tag == "!!merge" && key.Value == "<<" }
	seen := make(map[string]bool, len(mapping.Content)/2)
	for index := 0; index < len(mapping.Content); index += 2 {
		if !isMerge(mapping.Content[index]) {
			seen[keyID(mapping.Content[index])] = true
		}
	}
	var content []*yaml.Node
	for index := 0; index < len(mapping.Content); index += 2 {
		key, value := mapping.Content[index], mapping.Content[index+1]
		if !isMerge(key) {
			content = append(content, key, value)
			continue
		}
		inherited := []*yaml.Node{value}
		if value.Kind == yaml.SequenceNode {
			inherited = value.Content
		}
		for _, source := range inherited {
			if source.Kind != yaml.MappingNode {
				return fmt.Errorf("workspace_unavailable: invalid inherited Hermes YAML mapping")
			}
			for inheritedIndex := 0; inheritedIndex < len(source.Content); inheritedIndex += 2 {
				inheritedKey := source.Content[inheritedIndex]
				identity := keyID(inheritedKey)
				if !seen[identity] {
					seen[identity] = true
					content = append(content, inheritedKey, source.Content[inheritedIndex+1])
				}
			}
		}
	}
	mapping.Content = content
	return nil
}

func mergeDesktopWebYAMLNode(destination, source *yaml.Node) {
	if destination.Kind != yaml.MappingNode || source.Kind != yaml.MappingNode {
		*destination = *source
		return
	}
	for index := 0; index < len(source.Content); index += 2 {
		key, value := source.Content[index], source.Content[index+1]
		found := false
		for target := 0; target < len(destination.Content); target += 2 {
			if destination.Content[target].Value == key.Value {
				mergeDesktopWebYAMLNode(destination.Content[target+1], value)
				found = true
				break
			}
		}
		if !found {
			destination.Content = append(destination.Content, key, value)
		}
	}
}

func mergeDesktopWebEnv(existing []byte, managed map[string]string) ([]byte, error) {
	var lines []string
	var quote byte
	var replaceRecord bool
	for _, line := range strings.Split(strings.TrimSuffix(string(existing), "\n"), "\n") {
		if quote != 0 {
			quote = desktopWebEnvQuote(line, quote)
		} else {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "export ") || strings.HasPrefix(trimmed, "export\t") {
				trimmed = strings.TrimSpace(trimmed[len("export"):])
			}
			key, value, assignment := strings.Cut(trimmed, "=")
			assignment = assignment && !strings.HasPrefix(trimmed, "#")
			_, replaceRecord = managed[strings.TrimSpace(key)]
			replaceRecord = assignment && replaceRecord
			value = strings.TrimSpace(value)
			if assignment && len(value) != 0 && (value[0] == '\'' || value[0] == '"') {
				quote = desktopWebEnvQuote(value[1:], value[0])
			}
		}
		if !replaceRecord && (line != "" || len(existing) != 0) {
			lines = append(lines, line)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("workspace_unavailable: invalid quoted Hermes .env value; existing file preserved")
	}
	keys := make([]string, 0, len(managed))
	for key := range managed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		lines = append(lines, key+"="+quoteEnvValue(managed[key]))
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

func desktopWebEnvQuote(line string, quote byte) byte {
	escaped := false
	for index := 0; index < len(line); index++ {
		if escaped {
			escaped = false
			continue
		}
		if line[index] == '\\' {
			escaped = true
			continue
		}
		if line[index] == quote {
			return 0
		}
	}
	return quote
}

func mergeDesktopWebGatewayJSON(existing []byte, basePath, origin string, proxies []string) ([]byte, error) {
	data := map[string]json.RawMessage{}
	if len(existing) != 0 && (json.Unmarshal(existing, &data) != nil || data == nil) {
		return nil, fmt.Errorf("workspace_unavailable: invalid Hermes gateway.json; existing file preserved")
	}
	// gateway.json is compatibility metadata for the runtime; upstream Hermes
	// reads dashboard.public_url/trusted_proxies from config.yaml. ClawManager
	// must forward X-Forwarded-Prefix to preserve the classic Dashboard route.
	for key, value := range map[string]any{"base_path": basePath, "auth_mode": "basic", "allowed_origins": []string{origin}, "trusted_proxies": proxies} {
		encoded, _ := json.Marshal(value)
		data[key] = encoded
	}
	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("workspace_unavailable: encode Hermes gateway.json")
	}
	return append(payload, '\n'), nil
}
