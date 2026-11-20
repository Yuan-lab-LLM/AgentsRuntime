package process

import (
	"context"
	"net/url"
	"path"
	"strconv"
	"strings"
)

const taskProtocolFlag = "CLAWMANAGER_OPENCLAW_TASK_PROTOCOL"
const renewalFlag = "CLAWMANAGER_OPENCLAW_CAPABILITY_RENEWAL"
const enrollmentFlag = "CLAWMANAGER_OPENCLAW_CAPABILITY_ENROLLMENT"

// Reject duplicate/ambiguous flags before probing or changing workspace state.
func capabilityFlags(env []string) (map[string]string, error) {
	flags := map[string]string{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if key != taskProtocolFlag && key != renewalFlag && key != enrollmentFlag {
			continue
		}
		if _, exists := flags[key]; exists {
			return nil, errTaskActivation
		}
		if value != "" && value != "0" && value != "1" {
			return nil, errTaskActivation
		}
		flags[key] = value
	}
	if flags[renewalFlag] == "1" && flags[taskProtocolFlag] != "1" || flags[enrollmentFlag] == "1" && flags[renewalFlag] != "1" {
		return nil, errTaskActivation
	}
	return flags, nil
}

// Validate the actual child environment, after generated identity and storage
// paths have replaced request values. Never infer or enable a capability.
func checkRuntimeActivation(ctx context.Context, env []string, probe func(context.Context, []string) ([]byte, error)) error {
	flags, err := capabilityFlags(env)
	if err != nil {
		return err
	}
	if flags[renewalFlag] == "1" {
		values := map[string]string{}
		for _, entry := range env {
			key, value, _ := strings.Cut(entry, "=")
			switch key {
			case "CLAWMANAGER_LLM_BASE_URL", "CLAWMANAGER_LLM_API_KEY", "CLAWMANAGER_GATEWAY_GENERATION", "CLAWMANAGER_TASK_RECEIPT_DIR":
				if _, exists := values[key]; exists {
					return errTaskActivation
				}
				values[key] = value
			}
		}
		base := values["CLAWMANAGER_LLM_BASE_URL"]
		u, err := url.Parse(base)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.TrimSpace(base) != base {
			return errTaskActivation
		}
		key := values["CLAWMANAGER_LLM_API_KEY"]
		if key == "" || strings.TrimSpace(key) != key || strings.ContainsAny(key, "\r\n\x00") {
			return errTaskActivation
		}
		generation := values["CLAWMANAGER_GATEWAY_GENERATION"]
		n, err := strconv.ParseInt(generation, 10, 32)
		if err != nil || n < 0 || strconv.FormatInt(n, 10) != generation {
			return errTaskActivation
		}
		if flags[enrollmentFlag] == "1" {
			directory := values["CLAWMANAGER_TASK_RECEIPT_DIR"]
			// Linux image paths; filesystem ownership is checked by the existing
			// storage preparation layer and again by the runtime receipt store.
			if !path.IsAbs(directory) || directory == "/" || path.Clean(directory) != directory || strings.ContainsAny(directory, "\r\n\x00\\") {
				return errTaskActivation
			}
		}
	}
	return checkTaskActivation(ctx, env, probe)
}
