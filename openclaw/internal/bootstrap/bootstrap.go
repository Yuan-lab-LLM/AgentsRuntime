package bootstrap

import (
	"fmt"

	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
)

// Run executes the root-level initialization sequence that used to live in
// scripts/99-openclaw-sync, then drops privileges to cfg.DropUserName.
//
// When the process is not running as root, the filesystem-seeding steps
// execute as best-effort and both applyOwnership and dropPrivileges become
// no-ops. This keeps local development flows (manually invoking the agent
// as a non-root user) working.
func Run(cfg appconfig.Config) error {
	if err := syncDefaults(cfg); err != nil {
		return fmt.Errorf("sync defaults: %w", err)
	}
	if err := ensureExtensionsDir(cfg); err != nil {
		return fmt.Errorf("ensure extensions dir: %w", err)
	}
	if err := syncAutostart(cfg); err != nil {
		return fmt.Errorf("sync autostart: %w", err)
	}
	if err := ensureTeamSharedDirs(cfg); err != nil {
		return fmt.Errorf("ensure team shared dirs: %w", err)
	}
	if err := applyOwnership(cfg); err != nil {
		return fmt.Errorf("apply ownership: %w", err)
	}
	if err := ensureDingtalkOpenclawSymlink(cfg); err != nil {
		return fmt.Errorf("dingtalk openclaw symlink: %w", err)
	}
	raiseOpenFileLimit()
	if err := dropPrivileges(cfg); err != nil {
		return fmt.Errorf("drop privileges: %w", err)
	}
	return nil
}
