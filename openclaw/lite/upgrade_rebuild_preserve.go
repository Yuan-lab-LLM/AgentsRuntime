package openclaw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type preservedRebuild struct {
	InstanceID      int    `json:"instance_id"`
	Generation      int    `json:"generation"`
	OriginalPath    string `json:"original_path"`
	CandidatePath   string `json:"candidate_path"`
	OriginalSHA256  string `json:"original_sha256"`
	CandidateSHA256 string `json:"candidate_sha256,omitempty"`
	Installed       bool   `json:"installed"`
}

// Upgrade-only replacement. Factory reset/eraseWorkspace are deliberately not
// involved. The original state is retained, including transient files omitted
// from the fresh environment; projects outside .openclaw stay in place.
func (m *upgradeService) rebuildPreservingState(ctx context.Context, workspace string, req WorkspaceUpgradeRequest) (preservedRebuild, error) {
	var proof preservedRebuild
	if err := validateUpgradeID("rollout_id", req.RolloutID); err != nil {
		return proof, err
	}
	state := filepath.Join(workspace, "home", ".openclaw")
	dir := filepath.Join(m.configCapsuleDir(req), "preserved-rebuild")
	if err := m.io.MkdirAll(dir, 0700); err != nil {
		return proof, err
	}
	journal := filepath.Join(dir, "receipt.json")
	original, candidate := filepath.Join(dir, "original"), filepath.Join(dir, "candidate")
	if raw, err := m.io.ReadFile(journal); err == nil {
		if json.Unmarshal(raw, &proof) != nil || proof.InstanceID != req.InstanceID || proof.Generation != req.Generation || proof.OriginalPath != original || proof.CandidatePath != candidate || proof.OriginalSHA256 == "" {
			return proof, errors.New("invalid data-preserving rebuild receipt")
		}
		if proof.Installed {
			digest, err := m.preservedRebuildDigest(ctx, original)
			if err != nil || digest != proof.OriginalSHA256 {
				return proof, errors.New("retained original data verification failed")
			}
			return proof, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return proof, err
	} else {
		info, err := m.io.Lstat(state)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return proof, errors.New("original OpenClaw state is unavailable or unsafe")
		}
		// Refuse leftovers rather than treating an incomplete copy as a backup.
		if _, err := m.io.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
			return proof, errors.New("unverified rebuild candidate retained; inspection required")
		}
		if _, err := m.io.Lstat(original); !errors.Is(err, os.ErrNotExist) {
			return proof, errors.New("unexpected retained original; inspection required")
		}
		needed, err := m.rebuildTreeBytes(state)
		if err != nil {
			return proof, err
		}
		free, err := availableFilesystemBytes(workspace)
		if err != nil {
			return proof, err
		}
		if runtime.GOOS == "linux" && (free == 0 || uint64(needed)+(64<<20) > free) {
			return proof, errors.New("insufficient space to preserve instance data before rebuilding")
		}
		if err := m.io.Mkdir(candidate, 0700); err != nil {
			return proof, err
		}
		hash, err := m.copyPreservedRebuildTree(ctx, state, candidate, workspace)
		if err != nil {
			return proof, fmt.Errorf("original data unchanged; rebuild copy was not verified: %w", err)
		}
		if runtime.GOOS == "linux" {
			if err := m.chownTree(candidate, req.UID, req.GID); err != nil {
				return proof, err
			}
		}
		if err := m.rebuildImageManagedPlugins(candidate, "/defaults/.openclaw"); err != nil {
			return proof, fmt.Errorf("original data unchanged; fresh plugin environment failed validation: %w", err)
		}
		if runtime.GOOS == "linux" {
			if err := m.chownTree(candidate, req.UID, req.GID); err != nil {
				return proof, err
			}
		}
		candidateHash, err := m.preservedRebuildDigest(ctx, candidate)
		if err != nil {
			return proof, err
		}
		proof = preservedRebuild{InstanceID: req.InstanceID, Generation: req.Generation, OriginalPath: original, CandidatePath: candidate, OriginalSHA256: hash, CandidateSHA256: candidateHash}
		raw, _ := json.Marshal(proof)
		if err := m.io.AtomicWriteFile(journal, raw, 0600); err != nil {
			return proof, err
		}
	}
	if err := ctx.Err(); err != nil {
		return proof, err
	}
	// Resume either side of the two renames. No unlink/delete of either tree.
	_, origErr := m.io.Lstat(original)
	_, stateErr := m.io.Lstat(state)
	_, candidateErr := m.io.Lstat(candidate)
	verify := func(name string, expected string) error {
		digest, err := m.preservedRebuildDigest(ctx, name)
		if err != nil || digest != expected {
			return errors.New("rebuild cutover checksum mismatch; original data retained")
		}
		return nil
	}
	candidateHash := proof.CandidateSHA256
	if candidateHash == "" {
		candidateHash = proof.OriginalSHA256
	} // legacy receipt
	if origErr == nil {
		if err := verify(original, proof.OriginalSHA256); err != nil {
			return proof, err
		}
	} else if stateErr == nil {
		if err := verify(state, proof.OriginalSHA256); err != nil {
			return proof, err
		}
	}
	if candidateErr == nil {
		if err := verify(candidate, candidateHash); err != nil {
			return proof, err
		}
	} else if stateErr == nil && origErr == nil {
		if err := verify(state, candidateHash); err != nil {
			return proof, err
		}
	}
	if errors.Is(origErr, os.ErrNotExist) {
		if stateErr != nil || candidateErr != nil {
			return proof, errors.New("rebuild rename inputs changed")
		}
		if err := m.io.Rename(state, original); err != nil {
			return proof, err
		}
		stateErr = os.ErrNotExist
	} else if origErr != nil {
		return proof, origErr
	}
	if errors.Is(stateErr, os.ErrNotExist) {
		if candidateErr != nil {
			return proof, errors.New("rebuild original retained but candidate missing")
		}
		if err := m.io.Rename(candidate, state); err != nil {
			return proof, fmt.Errorf("original data retained; install candidate failed: %w", err)
		}
	} else if stateErr != nil || candidateErr == nil {
		return proof, errors.New("ambiguous rebuild cutover; originals retained")
	}
	proof.Installed = true
	raw, _ := json.Marshal(proof)
	return proof, m.io.AtomicWriteFile(journal, raw, 0600)
}

func (m *upgradeService) preservedRebuildDigest(ctx context.Context, root string) (string, error) {
	src, err := m.io.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer src.Close()
	hash := sha256.New()
	err = m.io.WalkDir(root, func(name string, e fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rebuildTransientRoot(rel) {
			if e.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := src.Lstat(rel)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s:%o:", filepath.ToSlash(rel), info.Mode())
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := src.Readlink(rel)
			if err != nil {
				return err
			}
			fmt.Fprint(hash, link)
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return errors.New("retained original contains a special file")
		}
		input, err := src.Open(rel)
		if err != nil {
			return err
		}
		fileHash := sha256.New()
		_, err = io.Copy(fileHash, input)
		input.Close()
		if err != nil {
			return err
		}
		hash.Write(fileHash.Sum(nil))
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func rebuildTransientRoot(rel string) bool {
	root := strings.Split(filepath.ToSlash(rel), "/")[0]
	return root == "tmp" || root == "cache" || root == "xdg-state"
}

func (m *upgradeService) rebuildTreeBytes(root string) (int64, error) {
	var size int64
	err := m.io.WalkDir(root, func(name string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, name)
		if rebuildTransientRoot(rel) {
			if e.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			size += info.Size()
		}
		return nil
	})
	return size, err
}

func (m *upgradeService) copyPreservedRebuildTree(ctx context.Context, source, target, workspace string) (string, error) {
	src, err := m.io.OpenRoot(source)
	if err != nil {
		return "", err
	}
	defer src.Close()
	dst, err := m.io.OpenRoot(target)
	if err != nil {
		return "", err
	}
	defer dst.Close()
	hash := sha256.New()
	err = m.io.WalkDir(source, func(name string, e fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, name)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rebuildTransientRoot(rel) {
			if e.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := src.Lstat(rel)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s:%o:", filepath.ToSlash(rel), info.Mode())
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := src.Readlink(rel)
			if err != nil {
				return err
			}
			resolved, err := m.upgradePluginLink(name, workspace)
			if err != nil {
				return err
			}
			if !pathWithin(workspace, resolved) && !pathWithin("/defaults/.openclaw", resolved) && !pathWithin("/usr/local/lib/node_modules/openclaw", resolved) {
				return errors.New("rebuild refuses external state link")
			}
			fmt.Fprint(hash, link)
			// Internal absolute links must not point back into the retained old
			// tree. Relative links work before and after the atomic HOME cutover.
			if filepath.IsAbs(link) && pathWithin(source, filepath.Clean(link)) {
				link, err = filepath.Rel(filepath.Dir(name), link)
				if err != nil {
					return err
				}
			}
			return dst.Symlink(link, rel)
		}
		if info.IsDir() {
			if err := dst.MkdirAll(rel, info.Mode().Perm()); err != nil {
				return err
			}
			return dst.Chmod(rel, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("rebuild refuses special file %s", rel)
		}
		input, err := src.Open(rel)
		if err != nil {
			return err
		}
		output, err := dst.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			input.Close()
			return err
		}
		a, b := sha256.New(), sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(output, a), input)
		closeErr := errors.Join(output.Sync(), output.Close(), input.Close())
		if copyErr != nil || closeErr != nil {
			return errors.Join(copyErr, closeErr)
		}
		if err := dst.Chmod(rel, info.Mode().Perm()); err != nil {
			return err
		}
		check, err := dst.Open(rel)
		if err != nil {
			return err
		}
		_, err = io.Copy(b, check)
		check.Close()
		if err != nil {
			return err
		}
		after, err := src.Lstat(rel)
		if err != nil {
			return err
		}
		if n != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) || !os.SameFile(info, after) || hex.EncodeToString(a.Sum(nil)) != hex.EncodeToString(b.Sum(nil)) {
			return errors.New("rebuild copy identity or checksum changed")
		}
		hash.Write(a.Sum(nil))
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
