package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"io"
	"os"
	"strings"
	"syscall"
)

// The auxiliary process has no trusted diagnostic transport besides stderr.
// Emit only fixed phase/errno categories, never errors containing config text.
func NativeStorageFailureCode(err error) string {
	phase := "helper"
	text := strings.ToLower(err.Error())
	for _, name := range []string{"credentials", "workspace", "config"} {
		if strings.Contains(text, "native_storage_"+name+":") {
			phase = name
			break
		}
	}
	code := "helper failed"
	var diagnostic interface{ SafeDiagnosticCode() string }
	if errors.As(err, &diagnostic) {
		for _, stage := range []string{"open_base", "resolve_workspace", "open_workspace", "open_directory", "list_directory", "read", "transform", "recheck", "replace"} {
			for _, kind := range []string{"generated", "yaml", "json", "env"} {
				if diagnostic.SafeDiagnosticCode() == stage+"_"+kind {
					return "native_storage_" + phase + ": " + stage + "_" + kind
				}
			}
		}
	}
	switch {
	case errors.Is(err, syscall.EACCES):
		code = "permission denied"
	case errors.Is(err, syscall.EPERM):
		code = "operation not permitted"
	case errors.Is(err, syscall.ENOENT):
		code = "no such file"
	case strings.Contains(text, "identity mismatch"):
		code = "native helper identity mismatch"
	case strings.Contains(text, "unsafe"):
		code = "unsafe configuration"
	case strings.Contains(text, "credential"):
		code = "credential configuration"
	}
	return "native_storage_" + phase + ": " + code
}

// NativeStorageConfig contains only configuration the instance already receives
// through its generated native files. Management credentials are not serializable.
type NativeStorageConfig struct {
	TeamGrant                                                          *NativeTeamGrant
	RuntimeType, WorkspaceRoot, SandboxProfile, NetworkSecurityProfile string
	OpenClawVersion, GatewayAuthMode, PublicOrigin, ProxyPolicyFile    string
	GatewayPortStart, GatewayPortEnd, GatewayPortBlockSize             int
	AllowedOrigins, TrustedProxies                                     []string
	LLMBaseURL, LLMAPIKeyEnvName                                       string
	LLMAPIKeySet, LLMModelsQualified                                   bool
	LLMModelIDs                                                        []string
	LLMVision, LLMReasoning                                            map[string]bool
	LLMReasoningControl                                                map[string]string
}

func NativeConfig(cfg Config) NativeStorageConfig {
	return NativeStorageConfig{
		RuntimeType: cfg.RuntimeType, WorkspaceRoot: cfg.WorkspaceRoot, SandboxProfile: cfg.SandboxProfile, NetworkSecurityProfile: cfg.NetworkSecurityProfile,
		GatewayPortStart: cfg.GatewayPortStart, GatewayPortEnd: cfg.GatewayPortEnd, GatewayPortBlockSize: cfg.GatewayPortBlockSize, OpenClawVersion: cfg.OpenClawVersion, GatewayAuthMode: cfg.GatewayAuthMode, PublicOrigin: cfg.PublicOrigin, ProxyPolicyFile: cfg.ProxyPolicyFile,
		AllowedOrigins: cfg.AllowedOrigins, TrustedProxies: cfg.TrustedProxies, LLMBaseURL: "http://clawmanager-local-model.invalid:31281/api/v1/gateway/llm", LLMAPIKeyEnvName: cfg.LLMAPIKeyEnvName, LLMAPIKeySet: cfg.LLMAPIKeySet, LLMModelsQualified: cfg.LLMModelsQualified, LLMModelIDs: cfg.LLMModelIDs, LLMVision: cfg.LLMVision, LLMReasoning: cfg.LLMReasoning, LLMReasoningControl: cfg.LLMReasoningControl,
	}
}
func (c NativeStorageConfig) config(profile RuntimeProfile) Config {
	return Config{Runtime: profile, RuntimeType: c.RuntimeType, WorkspaceRoot: c.WorkspaceRoot, SandboxProfile: c.SandboxProfile, NetworkSecurityProfile: c.NetworkSecurityProfile, GatewayPortStart: c.GatewayPortStart, GatewayPortEnd: c.GatewayPortEnd, GatewayPortBlockSize: c.GatewayPortBlockSize, OpenClawVersion: c.OpenClawVersion, GatewayAuthMode: c.GatewayAuthMode, PublicOrigin: c.PublicOrigin, ProxyPolicyFile: c.ProxyPolicyFile, AllowedOrigins: c.AllowedOrigins, TrustedProxies: c.TrustedProxies, LLMBaseURL: c.LLMBaseURL, LLMAPIKeyEnvName: c.LLMAPIKeyEnvName, LLMAPIKeySet: c.LLMAPIKeySet, LLMModelsQualified: c.LLMModelsQualified, LLMModelIDs: c.LLMModelIDs, LLMVision: c.LLMVision, LLMReasoning: c.LLMReasoning, LLMReasoningControl: c.LLMReasoningControl}
}

type NativeStorageRequest struct {
	Config  NativeStorageConfig
	Request CreateGatewayRequest
	Port    int
}
type PrivateStorageAuditRequest struct {
	UID, GID int
	TmpRoots []bool
}

type StorageGrantRequest struct {
	Root, RuntimeType, Team      string
	CapsuleRollout               string
	UserID, InstanceID, UID, GID int
}

func RunNativeStorageHelper(profile RuntimeProfile, args []string, in io.Reader, out io.Writer) (bool, error) {
	if len(args) == 1 && args[0] == "managed-native-workspace-health" {
		return true, runNativeWorkspaceHealth(profile, in)
	}
	if len(args) == 1 && args[0] == "managed-skill-scan" {
		return true, runNativeSkillScan(profile, in, out)
	}
	if len(args) == 1 && args[0] == "managed-native-upgrade" {
		return true, runNativeUpgradeHelper(profile, in, out)
	}
	if len(args) == 0 || (args[0] != "managed-native-storage" && args[0] != "managed-storage-grant" && args[0] != "managed-private-tree-audit") {
		return false, nil
	}
	if len(args) != 1 {
		return true, fmt.Errorf("invalid storage helper command")
	}
	decoder := json.NewDecoder(io.LimitReader(in, (2<<20)+1))
	decoder.DisallowUnknownFields()
	if args[0] == "managed-private-tree-audit" {
		if err := protectNativeStorageHelper(); err != nil {
			return true, err
		}
		var req PrivateStorageAuditRequest
		if err := decoder.Decode(&req); err != nil {
			return true, err
		}
		if req.GID != req.UID || len(req.TmpRoots) < 1 || len(req.TmpRoots) > 3 {
			return true, fmt.Errorf("invalid private audit scope")
		}
		for i, tmp := range req.TmpRoots {
			file := os.NewFile(uintptr(3+i), "private-grant")
			err := workspaceio.ValidateNFSPrivateFD(file, req.UID, tmp)
			file.Close()
			if err != nil {
				return true, err
			}
		}
		return true, nil
	}

	if args[0] == "managed-storage-grant" {
		var req StorageGrantRequest
		if err := decoder.Decode(&req); err != nil {
			return true, err
		}
		if os.Geteuid() != 10001 || os.Getegid() != req.GID {
			return true, fmt.Errorf("storage grant requires fixed management identity and assigned group")
		}
		if req.CapsuleRollout != "" {
			return true, workspaceio.PrepareNFSCapsule(req.Root, req.CapsuleRollout, req.InstanceID, req.GID)
		}
		if req.Team != "" {
			return true, workspaceio.PrepareNFSTeam(req.Root, req.UserID, req.Team, req.GID)
		}
		if err := workspaceio.PrepareNFSInstance(req.Root, req.RuntimeType, req.UserID, req.InstanceID, req.UID, req.GID); err != nil {
			return true, err
		}
		_, err := workspaceio.PrepareNFSTmp(req.Root, req.RuntimeType, req.UserID, req.InstanceID, req.UID, req.GID)
		return true, err
	}
	var op NativeStorageRequest
	if err := decoder.Decode(&op); err != nil {
		return true, err
	}
	req := op.Request
	cfg := op.Config.config(profile)
	cfg.NativeTeamGrant = op.Config.TeamGrant
	if profile == nil || cfg.SandboxProfile != "nfs-rootless-v1" || cfg.NetworkSecurityProfile != "isolated-network-v1" || req.SecurityProfile != cfg.NetworkSecurityProfile || req.AgentType != cfg.RuntimeType || req.Generation < 1 || op.Port < 1024 || op.Port > 65535 || profile.Type() != cfg.RuntimeType || req.UID < 200001 || req.GID != req.UID || os.Geteuid() != req.UID || os.Getegid() != req.GID {
		return true, fmt.Errorf("native helper identity mismatch")
	}
	if _, err := ValidateWorkspacePath(cfg.WorkspaceRoot, cfg.RuntimeType, req); err != nil {
		return true, err
	}
	if migrator, ok := profile.(ManagedCredentialMigrator); ok {
		if err := migrator.MigrateManagedCredentials(context.Background(), cfg, req, req.WorkspacePath); err != nil {
			return true, fmt.Errorf("native_storage_credentials: %w", err)
		}
	} else if cfg.NetworkSecurityProfile != "" {
		return true, fmt.Errorf("native credential migrator unavailable")
	}
	if err := profile.PrepareWorkspace(cfg, req, req.WorkspacePath); err != nil {
		return true, fmt.Errorf("native_storage_workspace: %w", err)
	}
	if err := profile.WriteGatewayConfig(cfg, req, req.WorkspacePath, op.Port); err != nil {
		return true, fmt.Errorf("native_storage_config: %w", err)
	}
	return true, nil
}
