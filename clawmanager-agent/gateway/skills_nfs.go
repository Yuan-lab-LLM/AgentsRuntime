package gateway

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/iamlovingit/clawmanager-agent/sandbox"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type NativeSkillScanRequest struct {
	RuntimeType, WorkspaceRoot   string
	UserID, InstanceID, UID, GID int
}

func runNativeSkillScan(profile RuntimeProfile, in io.Reader, out io.Writer) error {
	if err := protectNativeStorageHelper(); err != nil {
		return err
	}
	var scope NativeSkillScanRequest
	decoder := json.NewDecoder(io.LimitReader(in, 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&scope); err != nil {
		return err
	}
	if profile == nil || profile.Type() != scope.RuntimeType || scope.UserID < 1 || scope.InstanceID < 1 {
		return fmt.Errorf("invalid skill scan scope")
	}
	if err := workspaceio.ValidateInstanceCaller(scope.UID, scope.GID); err != nil {
		return err
	}
	workspace := filepath.Join(scope.WorkspaceRoot, scope.RuntimeType, fmt.Sprintf("user-%d", scope.UserID), fmt.Sprintf("instance-%d", scope.InstanceID))
	if _, err := ValidateWorkspacePath(scope.WorkspaceRoot, scope.RuntimeType, CreateGatewayRequest{UserID: scope.UserID, InstanceID: scope.InstanceID, WorkspacePath: workspace}); err != nil {
		return err
	}
	records, err := ScanSkillRootsChecked(workspace, skillRoots(Config{Runtime: profile}, workspace)...)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(records)
}

func scanInstanceSkills(cfg Config, state GatewayState) ([]SkillRecord, error) {
	if cfg.SandboxProfile != "nfs-rootless-v1" {
		return ScanSkillRoots(state.WorkspacePath, skillRoots(cfg, state.WorkspacePath)...), nil
	}
	if state.UserID < 1 || state.InstanceID < 1 || state.UID < 200001 || uint64(state.UID) >= 1<<32-1 || state.GID != state.UID {
		return nil, fmt.Errorf("skill scan identity unavailable")
	}
	expected, err := ValidateWorkspacePath(cfg.WorkspaceRoot, cfg.RuntimeType, CreateGatewayRequest{UserID: state.UserID, InstanceID: state.InstanceID, WorkspacePath: state.WorkspacePath})
	if err != nil {
		return nil, err
	}
	if err = workspaceio.CheckNFSSource(cfg.WorkspaceRoot, expected); err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(NativeSkillScanRequest{RuntimeType: cfg.RuntimeType, WorkspaceRoot: cfg.WorkspaceRoot, UserID: state.UserID, InstanceID: state.InstanceID, UID: state.UID, GID: state.GID})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "managed-skill-scan")
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8"}
	cmd.Stdin = bytes.NewReader(payload)
	configureNativeSkillScanCommand(cmd, state.UID, state.GID)
	var output limitedSkillOutput
	cmd.Stdout = &output
	if err = cmd.Run(); err != nil {
		return nil, fmt.Errorf("instance skill scan failed")
	}
	if output.truncated {
		return nil, fmt.Errorf("instance skill inventory exceeds limit")
	}
	var records []SkillRecord
	if err = json.Unmarshal(output.Bytes(), &records); err != nil {
		return nil, fmt.Errorf("invalid instance skill inventory")
	}
	return records, nil
}

type limitedSkillOutput struct {
	bytes.Buffer
	truncated bool
}

func (w *limitedSkillOutput) Write(data []byte) (int, error) {
	n := len(data)
	remaining := (1 << 20) - w.Len()
	if n > remaining {
		w.truncated = true
		data = data[:remaining]
	}
	w.Buffer.Write(data)
	return n, nil
}

// ScanSkillRootsChecked preserves the existing discovery format but propagates
// IO errors. An NFS failure must never become an authoritative empty inventory.
func ScanSkillRootsChecked(workspace string, roots ...string) ([]SkillRecord, error) {
	root, err := workspaceio.Open(workspace)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	read := func(path string) ([]byte, error) {
		file, err := openNativeSkillInput(root, path, false)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
		if len(data) > 8<<20 {
			return nil, fmt.Errorf("skill file exceeds limit")
		}
		return data, err
	}
	seen := map[string]bool{}
	records := []SkillRecord{}
	var walk func(string, string, int) error
	walk = func(current, prefix string, depth int) error {
		info, err := root.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill root requires a real directory")
		}
		directory, err := openNativeSkillInput(root, current, true)
		if err != nil {
			return err
		}
		entries, err := directory.ReadDir(-1)
		directory.Close()
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := strings.TrimSpace(entry.Name())
			if name == "" || strings.HasPrefix(name, ".") || !entry.IsDir() {
				continue
			}
			relative := name
			if prefix != "" {
				relative = prefix + "/" + name
			}
			if strings.Contains(relative, "..") {
				continue
			}
			skill := filepath.Join(current, name)
			manifest := false
			for _, name := range []string{"SKILL.md", "skill.json", "openclaw.skill.json"} {
				info, err := root.Lstat(filepath.Join(skill, name))
				if os.IsNotExist(err) {
					continue
				}
				if err != nil {
					return err
				}
				if !info.Mode().IsRegular() {
					return fmt.Errorf("skill manifest requires a regular file")
				}
				manifest = true
				break
			}
			if !manifest {
				if depth < skillDiscoveryMaxDepth {
					if err = walk(skill, relative, depth+1); err != nil {
						return err
					}
				}
				continue
			}
			if seen[skill] {
				continue
			}
			seen[skill] = true
			record := SkillRecord{SkillID: relative, Identifier: relative, InstallPath: skill, Source: "runtime", Type: "agent-skill"}
			for _, name := range []string{"skill.json", "openclaw.skill.json"} {
				data, err := read(filepath.Join(skill, name))
				if os.IsNotExist(err) {
					continue
				}
				if err != nil {
					return err
				}
				var meta map[string]any
				if json.Unmarshal(data, &meta) != nil {
					continue
				}
				if value := stringField(meta, "skill_id", "id", "name"); value != "" {
					record.SkillID = value
					record.Identifier = value
				}
				if value := stringField(meta, "identifier"); value != "" {
					record.Identifier = value
				}
				record.SkillVersion = stringField(meta, "skill_version", "version")
				break
			}
			data, err := read(filepath.Join(skill, "SKILL.md"))
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			if err == nil {
				sum := md5.Sum(data)
				record.ContentMD5 = hex.EncodeToString(sum[:])
			}
			records = append(records, record)
		}
		return nil
	}
	for _, path := range roots {
		if err = walk(filepath.Clean(path), "", 1); err != nil {
			return nil, err
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].InstallPath < records[j].InstallPath })
	return records, nil
}

func configureNativeSkillScanCommand(cmd *exec.Cmd, uid, gid int) {
	cmd.WaitDelay = time.Second
	configureGatewayCommand(cmd, uid, gid)
	cmd.Cancel = func() error { return sandbox.SignalProcess(cmd.Process, uint32(uid), os.Kill) }
}
