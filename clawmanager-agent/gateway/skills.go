package gateway

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const skillDiscoveryMaxDepth = 2

type skillDiscovery struct {
	RelativePath string
	SkillRoot    string
}

func BuildSkillReportPayload(cfg Config, manager *GatewayManager, podID int, mode string) SkillReportPayload {
	return BuildSkillReportPayloadForInstance(cfg, manager, podID, mode, 0)
}

func BuildSkillReportPayloadForInstance(cfg Config, manager *GatewayManager, podID int, mode string, instanceID int) SkillReportPayload {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = "full"
	}
	states := manager.GatewayStates()
	instances := make([]SkillInstanceReport, 0, len(states))
	for _, state := range states {
		if instanceID > 0 && state.InstanceID != instanceID {
			continue
		}
		instances = append(instances, SkillInstanceReport{
			InstanceID:    state.InstanceID,
			WorkspacePath: state.WorkspacePath,
			Skills:        ScanSkillRoots(state.WorkspacePath, skillRoots(cfg, state.WorkspacePath)...),
		})
	}
	if instanceID > 0 && len(instances) == 0 {
		instances = append(instances, SkillInstanceReport{
			InstanceID: instanceID,
			Skills:     []SkillRecord{},
		})
	}
	return SkillReportPayload{
		PodID:       podID,
		RuntimeType: cfg.RuntimeType,
		Namespace:   cfg.Namespace,
		PodName:     cfg.PodName,
		ReportedAt:  time.Now().UTC(),
		Mode:        mode,
		Instances:   instances,
	}
}

func skillRoots(cfg Config, workspace string) []string {
	if provider, ok := cfg.Runtime.(interface{ SkillRoots(string) []string }); ok {
		return provider.SkillRoots(workspace)
	}
	return []string{filepath.Join(workspace, "skills")}
}

func ScanSkillRoots(workspace string, roots ...string) []SkillRecord {
	bounded, err := workspaceio.Open(workspace)
	if err != nil {
		return nil
	}
	defer bounded.Close()
	seen := map[string]bool{}
	var skills []SkillRecord
	for _, root := range roots {
		discoveries := discoverSkillDirs(bounded, root, skillDiscoveryMaxDepth)
		for _, discovery := range discoveries {
			if seen[discovery.SkillRoot] {
				continue
			}
			seen[discovery.SkillRoot] = true
			skills = append(skills, readSkillRecord(bounded, discovery.SkillRoot, discovery.RelativePath))
		}
	}
	sort.Slice(skills, func(i, j int) bool {
		return skills[i].InstallPath < skills[j].InstallPath
	})
	return skills
}

func discoverSkillDirs(bounded *workspaceio.FS, root string, maxDepth int) []skillDiscovery {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" {
		return nil
	}
	if maxDepth <= 0 {
		maxDepth = skillDiscoveryMaxDepth
	}
	result := make([]skillDiscovery, 0)
	var walk func(currentRoot, relativePrefix string, depth int)
	walk = func(currentRoot, relativePrefix string, depth int) {
		entries, err := bounded.ReadDir(currentRoot)
		if err != nil {
			return
		}
		for _, entry := range entries {
			name := strings.TrimSpace(entry.Name())
			if name == "" || strings.HasPrefix(name, ".") || name == ".tmp" || !entry.IsDir() {
				continue
			}
			skillRoot := filepath.Join(currentRoot, name)
			relativePath := name
			if relativePrefix != "" {
				relativePath = relativePrefix + "/" + name
			}
			relativePath = strings.Trim(strings.ReplaceAll(relativePath, "\\", "/"), "/")
			if relativePath == "" || strings.Contains(relativePath, "..") {
				continue
			}
			if hasSkillManifest(bounded, skillRoot) {
				result = append(result, skillDiscovery{
					RelativePath: relativePath,
					SkillRoot:    skillRoot,
				})
				continue
			}
			if depth < maxDepth {
				walk(skillRoot, relativePath, depth+1)
			}
		}
	}
	walk(root, "", 1)
	return result
}

func hasSkillManifest(bounded *workspaceio.FS, skillRoot string) bool {
	for _, name := range []string{"SKILL.md", "skill.json", "openclaw.skill.json"} {
		info, err := bounded.Stat(filepath.Join(skillRoot, name))
		if err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func readSkillRecord(bounded *workspaceio.FS, installPath, fallbackID string) SkillRecord {
	fallbackID = strings.Trim(strings.ReplaceAll(strings.TrimSpace(fallbackID), "\\", "/"), "/")
	record := SkillRecord{
		SkillID:     fallbackID,
		Identifier:  fallbackID,
		InstallPath: installPath,
		Source:      "runtime",
		Type:        "agent-skill",
	}
	for _, metaName := range []string{"skill.json", "openclaw.skill.json"} {
		data, err := bounded.ReadFile(filepath.Join(installPath, metaName))
		if err != nil {
			continue
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
		if value := stringField(meta, "skill_version", "version"); value != "" {
			record.SkillVersion = value
		}
		break
	}
	record.ContentMD5 = contentMD5(bounded, filepath.Join(installPath, "SKILL.md"))
	return record
}

func stringField(values map[string]any, keys ...string) string {
	for _, key := range keys {
		raw, ok := values[key]
		if !ok {
			continue
		}
		value, ok := raw.(string)
		if ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func contentMD5(bounded *workspaceio.FS, path string) string {
	data, err := bounded.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}
