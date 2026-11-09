import fs from 'node:fs';
import path from 'node:path';

const packageRoot = process.env.OPENCLAW_PACKAGE_ROOT || '/usr/local/lib/node_modules/openclaw';
const core = JSON.parse(fs.readFileSync(path.join(packageRoot, 'package.json'), 'utf8'));
if (core.version !== '2026.10.2') throw new Error(`unsupported workspace overlay core ${core.version}`);
const dependency = path.join(packageRoot, 'node_modules', '@openclaw', 'fs-safe');
const pkg = JSON.parse(fs.readFileSync(path.join(dependency, 'package.json'), 'utf8'));
if (pkg.version !== '0.24.2') throw new Error(`unsupported workspace publication dependency ${pkg.version}`);
const file = path.join(dependency, 'dist', 'pinned-write-staged.js');
const marker = 'CLAWMANAGER_NFS_STAGED_CREATE_HANDOFF_V2';
const handoff = `            // ${marker}: unlinking an open stage on NFS creates a temporary
            // second link. Pin the published inode, close its stage, then unlink.
            // Original final hardlink, identity, permissions and root checks remain.
            if (deferredCleanup) {
                // Linux O_PATH pins metadata without read permission, including mode 000.
                // O_NOFOLLOW pins a substituted symlink itself; isFile rejects it below.
                readHandle = await fs.open(targetPath, 0x200000 | fsSync.constants.O_NOFOLLOW);
                const pinned = fsSync.fstatSync(readHandle.fd, { bigint: true });
                const named = fsSync.lstatSync(targetPath, { bigint: true });
                if (!pinned.isFile() || named.isSymbolicLink() || !named.isFile() ||
                    !sameFileIdentity(pinned, verificationIdentity) ||
                    !sameFileIdentity(named, verificationIdentity)) {
                    throw new FsSafeError("path-mismatch", "published create handoff changed file identity");
                }
                await handle.close();
                handle = undefined;
                const cleanup = deferredCleanup;
                deferredCleanup = undefined;
                cleanup();
            }
`;
let source = fs.readFileSync(file, 'utf8');
for (const anchor of [
  'const completeCreate = params.overwrite === false && params.input.kind !== "file" && params.input.stageBeforePublish === true;',
  'publishCopyStage({',
  'await params.verifyPublished?.((readHandle ?? handle).fd, verificationIdentity, parentGuard);',
  'assertPinnedWriteMode((readHandle ?? handle).fd, params.mode, params.private);',
  'close: [async () => { await readHandle?.close(); }, async () => { await handle?.close(); }]',
]) if (!source.includes(anchor)) throw new Error(`staged publication contract changed: ${anchor}`);
function apply(target, replacements, label) {
  let text = fs.readFileSync(target, 'utf8');
  for (const [before, after] of replacements) {
    if (text.includes(after)) continue;
    if (text.split(before).length !== 2) throw new Error(`${label} contract changed`);
    if (!process.argv.includes('--patch')) throw new Error(`${label} overlay is missing`);
    text = text.replace(before, after);
  }
  if (process.argv.includes('--patch')) fs.writeFileSync(target, text);
  for (const [, after] of replacements) if (!text.includes(after)) throw new Error(`${label} overlay differs`);
}
const copyFile = path.join(dependency, 'dist', 'publish-copy-stage.js');
const copySource = fs.readFileSync(copyFile, 'utf8');
for (const anchor of ['assertSyncDirectoryGuard(params.parentGuard);', 'staged.nlink !== 1n',
  '!sameFileIdentityForCleanup(current, identity)', 'fs.unlinkSync(temporaryPath);'])
  if (!copySource.includes(anchor)) throw new Error(`copy identity contract changed: ${anchor}`);
const cleanupStart = '    try {\n        const parent = fs.lstatSync(path.dirname(temporaryPath), { bigint: true });';
const cleanupEnd = '    if (observerRejected)\n        throw observerError;';
const originalCleanup = copySource.includes('CLAWMANAGER_NFS_DEFER_OWNED_CLEANUP_V2') ? undefined :
  copySource.slice(copySource.indexOf(cleanupStart), copySource.indexOf(cleanupEnd) + cleanupEnd.length);
if (!copySource.includes('CLAWMANAGER_NFS_DEFER_OWNED_CLEANUP_V2')) {
  if (!originalCleanup?.startsWith(cleanupStart) || !originalCleanup.endsWith(cleanupEnd)) throw new Error('copy cleanup contract changed');
  apply(copyFile, [[originalCleanup, `    // CLAWMANAGER_NFS_DEFER_OWNED_CLEANUP_V2: default callers keep synchronous cleanup.
    const cleanup = () => {
${originalCleanup}
    };
    if (params.deferTemporaryCleanup && !observerRejected)
        params.deferTemporaryCleanup(cleanup);
    else
        cleanup();`]], 'copy cleanup');
} else {
  for (const part of [cleanupStart, cleanupEnd, 'params.deferTemporaryCleanup(cleanup);', 'else\n        cleanup();'])
    if (!copySource.includes(part)) throw new Error('copy cleanup overlay differs');
}
apply(file, [
  ['    let renamed = false;', '    let renamed = false;\n    let deferredCleanup;'],
  ['    let receipt;', '    const settleNfsStage = completeCreate && process.platform === "linux" && fsSync.statfsSync(parentPath).type === 0x6969;\n    let receipt;'],
  ['                    assertBeforeMutation,\n                    onPublicationAttempt:', '                    assertBeforeMutation,\n                    deferTemporaryCleanup: settleNfsStage ? cleanup => { deferredCleanup = cleanup; } : undefined,\n                    onPublicationAttempt:'],
  ['            await getFsSafeTestHooks()?.afterPinnedWriteFallbackRename?.(targetPath);', handoff + '            await getFsSafeTestHooks()?.afterPinnedWriteFallbackRename?.(targetPath);'],
  ['                    if (renamed) {', '                    if (renamed) {\n                        if (deferredCleanup) {\n                            const cleanup = deferredCleanup;\n                            deferredCleanup = undefined;\n                            cleanup();\n                        }'],
], 'staged NFS publication');
source = fs.readFileSync(file, 'utf8');
if (!source.includes(handoff) || source.split(marker).length !== 2) throw new Error('workspace publication overlay differs');
console.log(`Verified OpenClaw ${core.version}/fs-safe ${pkg.version} NFS atomic create handoff`);

// Doctor uses the standalone no-replace move, not the staged-create path above.
// Keep custody through a verified target pin before closing the source, then
// remove only its identity-matched name beneath the pinned source parent.
const moveFile = path.join(dependency, 'dist', 'publish-file.js');
apply(moveFile, [
[
  '    const source = await fs.open(sourcePath, resolveReadOpenFlags());',
  '    let source = await fs.open(sourcePath, resolveReadOpenFlags());',
], [
  '        if (strategy === "rename-noreplace") {\n            const binding = requireNativeBinding();\n            publishFileNoReplaceNative',
  `        // CLAWMANAGER_NFS_EXCLUSIVE_MOVE_HANDOFF_V2
        if (strategy === "rename-noreplace" && process.platform === "linux" &&
            fsSync.statfsSync(path.dirname(sourcePath)).type === 0x6969) {
            const binding = requireNativeBinding();
            if (!binding.rootRemovalUnlink) throw new FsSafeError("helper-unavailable", "identity-fenced NFS unlink unavailable");
            if (sourceExactIdentity.nlink !== 1n) throw new FsSafeError("hardlink", "NFS move source has multiple links");
            assertSourceCurrent();
            binding.linkBeneath(sourceNativeParent.handle.fd, sourceNativeParent.basename,
                targetNativeParent.handle.fd, targetNativeParent.basename);
            rememberCreatedTarget(failure, sourceExactIdentity, "rename-verify");
            failure.preserveTarget = true;
            failure.fallback = "link-unlink";
            let targetPin;
            try {
                targetPin = await fs.open(targetPath, 0x200000 | fsSync.constants.O_NOFOLLOW);
                const pinned = fsSync.fstatSync(targetPin.fd, { bigint: true });
                const named = fsSync.lstatSync(targetPath, { bigint: true });
                const sourceNamed = fsSync.lstatSync(sourcePath, { bigint: true });
                if (!pinned.isFile() || !named.isFile() || named.isSymbolicLink() ||
                    !sourceNamed.isFile() || sourceNamed.isSymbolicLink() || pinned.nlink !== 2n ||
                    !sameFileIdentity(pinned, sourceExactIdentity) ||
                    !sameFileIdentity(named, sourceExactIdentity) || !sameFileIdentity(sourceNamed, sourceExactIdentity))
                    throw new FsSafeError("path-mismatch", "NFS move changed before handle handoff");
                await parent.assertCurrent();
                await source.close();
                source = targetPin;
                targetPin = undefined;
                binding.rootRemovalUnlink(sourceNativeParent.handle.fd, sourceNativeParent.basename,
                    sourceExactIdentity.dev, sourceExactIdentity.ino, false);
                failure.sourceRemoval = "removed";
                const settled = fsSync.fstatSync(source.fd, { bigint: true });
                if (settled.nlink !== 1n) throw new FsSafeError("hardlink", "NFS move target has unexpected links");
                await getFsSafeTestHooks()?.afterPublishTargetCreated?.("rename-noreplace", targetPath, sourceExactIdentity);
                assertPublishedTargetCurrent(targetPath, sourceExactIdentity, "NFS move publication target changed");
                assertRenamedSourceAbsent(sourcePath);
                syncFileBestEffortSync(sourceNativeParent.handle.fd);
                const receipt = await finishPublication("rename-noreplace", source, sourceExactIdentity);
                if (fsSync.fstatSync(source.fd, { bigint: true }).nlink !== 1n)
                    throw new FsSafeError("hardlink", "NFS move target gained links during sync");
                return receipt;
            } finally {
                await targetPin?.close().catch(() => undefined);
            }
        }
        if (strategy === "rename-noreplace") {
            const binding = requireNativeBinding();
            publishFileNoReplaceNative`,
]], 'NFS exclusive move');
console.log('Verified NFS exclusive move handoff with no-clobber, inode identity and final link-count fences');
