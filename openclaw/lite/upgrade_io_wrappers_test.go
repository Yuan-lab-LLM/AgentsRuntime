package openclaw

import (
	"context"

	"io"

	"os"

	"time"
)

func pluginProjectMetadata(project string) (string, string, string, error) {
	return (&upgradeService{io: newFixtureUpgradeIO(project)}).pluginProjectMetadata(project)
}
func chownTree(root string, uid, gid int) error {
	return (&upgradeService{io: newFixtureUpgradeIO(root, uid, gid)}).chownTree(root, uid, gid)
}
func runOpenClawCommandInHome(ctx context.Context, dir, home, configPath string, req WorkspaceUpgradeRequest, phase string, args []string, hash io.Writer, timeout time.Duration) error {
	return (&upgradeService{io: newFixtureUpgradeIO(ctx, dir, home, configPath, req, phase, args, hash, timeout)}).runOpenClawCommandInHome(ctx, dir, home, configPath, req, phase, args, hash, timeout)
}
func upgradePluginLink(name, stateRoot string) (string, error) {
	return (&upgradeService{io: newFixtureUpgradeIO(name, stateRoot)}).upgradePluginLink(name, stateRoot)
}
func seedImagePluginTreeWithin(image, dest, approvedRoot string) error {
	return (&upgradeService{io: newFixtureUpgradeIO(image, dest, approvedRoot)}).seedImagePluginTreeWithin(image, dest, approvedRoot)
}
func validateUpgradePluginLinks(state string) error {
	return (&upgradeService{io: newFixtureUpgradeIO(state)}).validateUpgradePluginLinks(state)
}
func runOpenClawDoctorRepair(ctx context.Context, dir, home, configPath string, req WorkspaceUpgradeRequest, hash io.Writer) error {
	return (&upgradeService{io: newFixtureUpgradeIO(ctx, dir, home, configPath, req, hash)}).runOpenClawDoctorRepair(ctx, dir, home, configPath, req, hash)
}
func openClawDoctorMigrationFingerprint(home, configPath string) (string, error) {
	return (&upgradeService{io: newFixtureUpgradeIO(home, configPath)}).openClawDoctorMigrationFingerprint(home, configPath)
}
func preservedRebuildDigest(ctx context.Context, root string) (string, error) {
	return (&upgradeService{io: newFixtureUpgradeIO(ctx, root)}).preservedRebuildDigest(ctx, root)
}
func copyTreeStable(source, target string) (int64, int64, error) {
	return (&upgradeService{io: newFixtureUpgradeIO(source, target)}).copyTreeStable(source, target)
}
func rebuildImageManagedPlugins(state, defaults string) error {
	return (&upgradeService{io: newFixtureUpgradeIO(state, defaults)}).rebuildImageManagedPlugins(state, defaults)
}
func planManagedPlugins(state, defaults string) (managedPluginPlan, error) {
	return (&upgradeService{io: newFixtureUpgradeIO(state, defaults)}).planManagedPlugins(state, defaults)
}
func captureUpgradeStateFile(source, rel, capsuleDir string, capsule *upgradeStateCapsule) error {
	return (&upgradeService{io: newFixtureUpgradeIO(source, rel, capsuleDir, capsule)}).captureUpgradeStateFile(source, rel, capsuleDir, capsule)
}
func managedPackageImageTarget(pkg, project, base string) (string, string, error) {
	return (&upgradeService{io: newFixtureUpgradeIO(pkg, project, base)}).managedPackageImageTarget(pkg, project, base)
}
func quarantineUpgradeTarget(target, failed string) error {
	return (&upgradeService{io: newFixtureUpgradeIO(target, failed)}).quarantineUpgradeTarget(target, failed)
}
func sessionMigrationArchiveStats(home string) (int64, int64, error) {
	return (&upgradeService{io: newFixtureUpgradeIO(home)}).sessionMigrationArchiveStats(home)
}
func removeUpgradeProbe(path string) {
	(&upgradeService{io: newFixtureUpgradeIO(path)}).removeUpgradeProbe(path)
}
func runOpenClawUpgradeCommand(ctx context.Context, workspace, home string, req WorkspaceUpgradeRequest, phase string, args []string, hash io.Writer) error {
	return (&upgradeService{io: newFixtureUpgradeIO(ctx, workspace, home, req, phase, args, hash)}).runOpenClawUpgradeCommand(ctx, workspace, home, req, phase, args, hash)
}
func applyManagedPluginPlan(state, defaults string, plan managedPluginPlan) error {
	return (&upgradeService{io: newFixtureUpgradeIO(state, defaults, plan)}).applyManagedPluginPlan(state, defaults, plan)
}
func writeSessionMigrationReceipt(path string, result SessionSQLiteMigrationResult) error {
	return (&upgradeService{io: newFixtureUpgradeIO(path, result)}).writeSessionMigrationReceipt(path, result)
}
func seedImagePluginTree(image, dest string) error {
	return (&upgradeService{io: newFixtureUpgradeIO(image, dest)}).seedImagePluginTree(image, dest)
}
func copyUpgradePluginTree(source, target, stateRoot string) (int64, int64, error) {
	return (&upgradeService{io: newFixtureUpgradeIO(source, target, stateRoot)}).copyUpgradePluginTree(source, target, stateRoot)
}
func copyRegularFileStable(source, target string, mode os.FileMode) error {
	return (&upgradeService{io: newFixtureUpgradeIO(source, target, mode)}).copyRegularFileStable(source, target, mode)
}
func rebuildTreeBytes(root string) (int64, error) {
	return (&upgradeService{io: newFixtureUpgradeIO(root)}).rebuildTreeBytes(root)
}
func copyPreservedRebuildTree(ctx context.Context, source, target, workspace string) (string, error) {
	return (&upgradeService{io: newFixtureUpgradeIO(ctx, source, target, workspace)}).copyPreservedRebuildTree(ctx, source, target, workspace)
}
