package openclaw

import "path/filepath"

func (Profile) SkillRoots(workspace string) []string {
	return []string{filepath.Join(workspace, "skills"), filepath.Join(workspace, "home", ".openclaw", "workspace", "skills"), filepath.Join(workspace, ".openclaw", "workspace", "skills")}
}
