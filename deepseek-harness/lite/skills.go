package deepseek

import "path/filepath"

func (Profile) SkillRoots(workspace string) []string {
	return []string{filepath.Join(workspace, "skills"), filepath.Join(workspace, "home", ".dsh", "skills")}
}
