package managedconfig

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

const ModelURL = "http://clawmanager-local-model.invalid:31281/api/v1/gateway/llm"
const ServicesURL = "http://clawmanager-local-services.invalid:31282"
const ModelKey = "cm-local-model"
const TeamKey = "cm-local-team"

func SafeEnvironment(cfg gateway.Config, req gateway.CreateGatewayRequest) (map[string]string, error) {
	if cfg.NetworkSecurityProfile != "isolated-network-v1" || req.SecurityProfile != cfg.NetworkSecurityProfile {
		return nil, ErrUnsafe
	}
	get := func(k string) string { v, _ := gateway.RequestEnvValue(req, k); return v }
	ui := get("CLAWMANAGER_INSTANCE_TOKEN")
	if ui == "" || strings.ContainsAny(ui, "\r\n\x00") || get("CLAWMANAGER_LLM_API_KEY") != ModelKey || get("CLAWMANAGER_LLM_BASE_URL") != ModelURL {
		return nil, ErrUnsafe
	}
	values := map[string]string{}
	for _, k := range []string{"CLAWMANAGER_LLM_API_KEY", "OPENAI_API_KEY"} {
		values[k] = ModelKey
	}
	for _, k := range []string{"CLAWMANAGER_LLM_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE", "CUSTOM_BASE_URL"} {
		values[k] = ModelURL
	}
	for _, k := range []string{"CLAWMANAGER_INSTANCE_TOKEN", "CLAWMANAGER_INSTANCE_ACCESS_TOKEN", "OPENCLAW_GATEWAY_TOKEN", "CLAWMANAGER_GATEWAY_TOKEN", "OPENCLAW_GATEWAY_PASSWORD", "OPENCODE_SERVER_PASSWORD"} {
		values[k] = ui
	}
	values["CLAWMANAGER_TEAM_TOKEN"] = TeamKey
	values["CLAWMANAGER_MANAGED_SERVICES_URL"] = ServicesURL
	values["CLAWMANAGER_NETWORK_ISOLATED"] = "1"
	redis := get("CLAWMANAGER_TEAM_REDIS_URL")
	if redis != "" {
		u, err := url.Parse(redis)
		if err != nil || u.User != nil || u.Scheme != "redis" || u.Host != "clawmanager-local-services.invalid:31283" || u.RawQuery != "" || u.Fragment != "" {
			return nil, ErrUnsafe
		}
		if _, err = LocalRedis(redis, ""); err != nil {
			return nil, err
		}
	}
	for _, k := range []string{"CLAWMANAGER_TEAM_REDIS_URL", "TEAM_REDIS_URL", "REDIS_URL"} {
		values[k] = redis
	}
	return values, nil
}
func LocalRedis(previous, trusted string) (string, error) {
	if trusted != "" {
		previous = trusted
	}
	u, err := url.Parse(previous)
	if err != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", ErrUnsafe
	}
	path := u.Path
	if path != "" && path != "/" {
		db, err := strconv.ParseUint(strings.TrimPrefix(path, "/"), 10, 31)
		if err != nil || !strings.HasPrefix(path, "/") {
			return "", ErrUnsafe
		}
		path = "/" + strconv.FormatUint(db, 10)
	}
	return "redis://clawmanager-local-services.invalid:31283" + path, nil
}
func forbiddenEnv(key string) bool {
	return strings.HasPrefix(key, "RUNTIME_AGENT_") || strings.HasPrefix(key, "RUNTIME_SLOT_") || (strings.HasPrefix(key, "CLAWMANAGER_AGENT_") && key != "CLAWMANAGER_AGENT_PERSISTENT_DIR")
}

// Environment replaces every duplicate managed assignment, including quoted
// multiline records; unrelated assignments and comments are retained verbatim.
func Environment(data []byte, values map[string]string) ([]byte, error) {
	return EnvironmentWithPolicy(data, values, nil)
}

// EnvironmentWithPolicy allows native adapters to replace individual additional
// records based on their old literal value. rawValue retains dotenv quoting;
// policy does not run for the unconditional managed keys in values.
func EnvironmentWithPolicy(data []byte, values map[string]string, policy func(key, rawValue string) (string, bool)) ([]byte, error) {
	var out []string
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		start := i
		trim := strings.TrimSpace(lines[i])
		if trim == "" || strings.HasPrefix(trim, "#") {
			out = append(out, lines[i])
			continue
		}
		if strings.HasPrefix(trim, "export ") || strings.HasPrefix(trim, "export\t") {
			trim = strings.TrimSpace(trim[6:])
		}
		key, value, ok := strings.Cut(trim, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t'\"$;()") {
			return nil, ErrUnsafe
		}
		value = strings.TrimSpace(value)
		if len(value) > 0 && (value[0] == '\'' || value[0] == '"') {
			quote := value[0]
			part := value[1:]
			closed := false
			for {
				escaped := false
				for j := 0; j < len(part); j++ {
					if escaped {
						escaped = false
						continue
					}
					if part[j] == '\\' {
						escaped = true
						continue
					}
					if part[j] == quote {
						closed = true
						break
					}
				}
				if closed {
					break
				}
				i++
				if i >= len(lines) {
					return nil, ErrUnsafe
				}
				part = lines[i]
			}
		}
		if forbiddenEnv(key) {
			continue
		}
		replacement, managed := values[key]
		if !managed && policy != nil {
			raw := value
			if i > start {
				raw += "\n" + strings.Join(lines[start+1:i+1], "\n")
			}
			replacement, managed = policy(key, raw)
		}
		if !managed {
			out = append(out, lines[start:i+1]...)
			continue
		}
		if key == "CLAWMANAGER_TEAM_REDIS_URL" || key == "TEAM_REDIS_URL" || key == "REDIS_URL" {
			var err error
			replacement, err = LocalRedis(strings.Trim(value, "\"'"), replacement)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, key+"="+envQuote(replacement))
	}
	return []byte(strings.Join(out, "\n") + "\n"), nil
}
func envQuote(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\r\n\"'#$`\\") {
		return strconv.Quote(s)
	}
	return s
}
func JSON(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	remaining := 100000
	var parse func(int) (any, error)
	parse = func(depth int) (any, error) {
		remaining--
		if remaining < 0 || depth > 64 {
			return nil, ErrUnsafe
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, ErrUnsafe
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return token, nil
		}
		switch delim {
		case '{':
			result := map[string]any{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, ErrUnsafe
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, ErrUnsafe
				}
				if _, duplicate := result[key]; duplicate {
					return nil, ErrUnsafe
				}
				value, err := parse(depth + 1)
				if err != nil {
					return nil, err
				}
				result[key] = value
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return nil, ErrUnsafe
			}
			return result, nil
		case '[':
			result := []any{}
			for decoder.More() {
				value, err := parse(depth + 1)
				if err != nil {
					return nil, err
				}
				result = append(result, value)
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return nil, ErrUnsafe
			}
			return result, nil
		default:
			return nil, ErrUnsafe
		}
	}
	value, err := parse(0)
	if err != nil {
		return nil, err
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, ErrUnsafe
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, ErrUnsafe
	}
	return object, nil
}
func EncodeJSON(value map[string]any) ([]byte, error) {
	b, err := json.MarshalIndent(value, "", "  ")
	return append(b, '\n'), err
}

// ManagedModelEndpoint recognizes the management API path, never a credential
// substring or arbitrary vendor/provider name.
func ManagedModelEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && strings.TrimSuffix(u.Path, "/") == "/api/v1/gateway/llm" && u.Hostname() != ""
}
func Generated(name, primary string) bool {
	return name == primary || strings.HasPrefix(name, primary+".bak") || strings.HasPrefix(name, primary+".backup") || name == primary+".clawmanager-pre-desktop-web.bak" || strings.HasPrefix(name, primary+".tmp")
}

// GeneratedTemporary matches the exact nonce format used by workspaceio and
// the Hermes writer. Similar user filenames are not selected.
func GeneratedTemporary(name, primary string) bool {
	prefix := ".cm-"
	if primary != "" {
		prefix = "." + primary + "."
	}
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".tmp") {
		return false
	}
	nonce := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".tmp")
	if len(nonce) != 24 {
		return false
	}
	for _, c := range nonce {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// ObjectPath accepts absent optional schema fields but rejects a conflicting
// shape; callers must not silently preserve a secret hidden in an invalid type.
func ObjectPath(object map[string]any, keys ...string) (map[string]any, error) {
	for _, key := range keys {
		value, exists := object[key]
		if !exists {
			return nil, nil
		}
		next, ok := value.(map[string]any)
		if !ok {
			return nil, ErrUnsafe
		}
		object = next
	}
	return object, nil
}
