package instanceagent

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxInstalledSkillFiles = 10000
	maxInstalledSkillBytes = 512 << 20
)

func InstallSkillArchive(skillRoot string, payload map[string]any, archive []byte) (map[string]any, error) {
	skillRoot = filepath.Clean(strings.TrimSpace(skillRoot))
	if skillRoot == "" || !filepath.IsAbs(skillRoot) {
		return nil, fmt.Errorf("skill install root must be absolute: %s", skillRoot)
	}
	targetName := firstPayloadString(payload, "target_name", "identifier", "skill_name")
	if targetName == "" {
		targetName = firstPayloadString(payload, "skill_id")
	}
	if err := validateSkillName(targetName); err != nil {
		return nil, err
	}
	targetPath := filepath.Join(skillRoot, targetName)
	if err := ensurePathWithin(skillRoot, targetPath); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		return nil, err
	}

	staging, err := os.MkdirTemp(skillRoot, ".install-"+targetName+"-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	if err := extractSkillZIP(archive, staging); err != nil {
		return nil, err
	}
	if !isSkillDirectory(staging) {
		return nil, errors.New("skill archive must contain SKILL.md, skill.json, or manifest.json")
	}
	contentMD5, _, _, err := ContentMD5(staging)
	if err != nil {
		return nil, err
	}
	if expected := strings.TrimSpace(firstPayloadString(payload, "content_md5", "content_hash", "md5")); expected != "" && !strings.EqualFold(expected, contentMD5) {
		return nil, fmt.Errorf("skill md5 mismatch: expected %s got %s", expected, contentMD5)
	}
	if err := replaceDirectory(staging, targetPath); err != nil {
		return nil, err
	}
	return map[string]any{
		"status":        "installed",
		"install_path":  targetPath,
		"skill_id":      firstPayloadString(payload, "skill_id"),
		"skill_version": firstPayloadString(payload, "skill_version", "skill_version_id"),
		"content_md5":   contentMD5,
	}, nil
}

func UninstallSkill(skillRoot string, payload map[string]any) (map[string]any, error) {
	skillRoot = filepath.Clean(strings.TrimSpace(skillRoot))
	targetName := firstPayloadString(payload, "target_name", "identifier", "skill_name", "skill_id")
	if targetPath := strings.TrimSpace(firstPayloadString(payload, "target_path", "install_path")); targetPath != "" {
		if err := ensurePathWithin(skillRoot, targetPath); err != nil {
			return nil, err
		}
		targetName = filepath.Base(filepath.Clean(targetPath))
	}
	if err := validateSkillName(targetName); err != nil {
		return nil, err
	}
	targetPath := filepath.Join(skillRoot, targetName)
	if err := ensurePathWithin(skillRoot, targetPath); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(targetPath); err != nil {
		return nil, err
	}
	return map[string]any{"status": "removed", "install_path": targetPath}, nil
}

func extractSkillZIP(archive []byte, target string) error {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return fmt.Errorf("open skill zip: %w", err)
	}
	if len(reader.File) == 0 || len(reader.File) > maxInstalledSkillFiles {
		return fmt.Errorf("skill zip contains invalid file count: %d", len(reader.File))
	}
	prefix := commonArchiveRoot(reader.File)
	var total uint64
	for _, item := range reader.File {
		name := strings.ReplaceAll(item.Name, "\\", "/")
		if strings.HasPrefix(name, "/") || archivePathHasTraversal(name) {
			return fmt.Errorf("unsafe skill zip path: %s", item.Name)
		}
		name = strings.TrimPrefix(name, prefix)
		name = strings.TrimPrefix(name, "/")
		if name == "" {
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(name))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe skill zip path: %s", item.Name)
		}
		if item.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill zip symlink is not allowed: %s", item.Name)
		}
		output := filepath.Join(target, clean)
		if err := ensurePathWithin(target, output); err != nil {
			return err
		}
		if item.FileInfo().IsDir() {
			if err := os.MkdirAll(output, 0o755); err != nil {
				return err
			}
			continue
		}
		total += item.UncompressedSize64
		if total > maxInstalledSkillBytes {
			return errors.New("uncompressed skill exceeds 512 MiB limit")
		}
		if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
			return err
		}
		source, err := item.Open()
		if err != nil {
			return err
		}
		destination, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			source.Close()
			return err
		}
		_, copyErr := io.CopyN(destination, source, int64(item.UncompressedSize64)+1)
		closeErr := destination.Close()
		source.Close()
		if copyErr != nil && !errors.Is(copyErr, io.EOF) {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func archivePathHasTraversal(name string) bool {
	for _, segment := range strings.Split(name, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func commonArchiveRoot(items []*zip.File) string {
	root := ""
	for _, item := range items {
		name := strings.Trim(strings.ReplaceAll(item.Name, "\\", "/"), "/")
		if name == "" {
			continue
		}
		parts := strings.Split(name, "/")
		if len(parts) < 2 {
			if item.FileInfo().IsDir() {
				if root == "" {
					root = parts[0]
				} else if root != parts[0] {
					return ""
				}
				continue
			}
			return ""
		}
		if root == "" {
			root = parts[0]
		} else if root != parts[0] {
			return ""
		}
	}
	if root == "" {
		return ""
	}
	return root + "/"
}

func replaceDirectory(staging, target string) error {
	backup := target + fmt.Sprintf(".backup-%d", time.Now().UnixNano())
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(staging, target); err != nil {
		_ = os.Rename(backup, target)
		return err
	}
	_ = os.RemoveAll(backup)
	return nil
}

func ensurePathWithin(root, target string) error {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("skill path %s is outside install root %s", target, root)
	}
	return nil
}

func validateSkillName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("skill target_name or identifier is required")
	}
	if name == "." || name == ".." || filepath.Base(name) != name || strings.HasPrefix(name, ".") {
		return fmt.Errorf("invalid skill target name: %s", name)
	}
	return nil
}

func firstPayloadString(payload map[string]any, keys ...string) string {
	values := payloadStrings(payload, keys...)
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}
