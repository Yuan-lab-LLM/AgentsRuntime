package opencode

import (
	"context"
	"errors"
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"os"
	"sync"
	"time"
)

type nativePayloadPaths struct {
	binary, attestation, python string
	// Only the scoped maintenance entry supplies an actor. Registration always
	// measures root-owned image files in the outer namespace.
	scopedUID, scopedGID int
	scopedProof          *gateway.NativeUpgradeParentProof
	scopedRoots          [2]string
}

var deliveredNativePayload = nativePayloadPaths{
	binary: "/usr/local/bin/opencode", attestation: "/usr/local/share/clawmanager/opencode-native-preserve-attestation.json", python: "/usr/bin/python3",
}

type nativePayloadKey struct {
	binary, attestation             os.FileInfo
	binaryChange, attestationChange string
}

func readNativePayloadKey(paths nativePayloadPaths) (key nativePayloadKey, err error) {
	if paths.scopedUID != 0 {
		if err = validateNativePayloadNamespace(paths.scopedUID, paths.scopedGID, paths.scopedProof, paths.scopedRoots); err != nil {
			return key, err
		}
	}
	key.binary, err = os.Lstat(paths.binary)
	if err != nil {
		return key, err
	}
	key.attestation, err = os.Lstat(paths.attestation)
	if err != nil {
		return key, err
	}
	key.binaryChange, err = paths.change(paths.binary, key.binary)
	if err != nil {
		return key, err
	}
	key.attestationChange, err = paths.change(paths.attestation, key.attestation)
	return key, err
}

func (key nativePayloadKey) matches(other nativePayloadKey) bool {
	same := func(a, b os.FileInfo) bool {
		return os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
	}
	return key.binaryChange == other.binaryChange && key.attestationChange == other.attestationChange &&
		same(key.binary, other.binary) && same(key.attestation, other.attestation)
}

type nativePayloadProofCache struct {
	mu      sync.Mutex
	checked bool
	key     nativePayloadKey
	err     error
}

func (cache *nativePayloadProofCache) check(ctx context.Context, paths nativePayloadPaths, validate func(context.Context) error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := unsupportedOpenCodeNativeEnvironment(); err != nil {
		return err
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	key, err := readNativePayloadKey(paths)
	if err != nil {
		cache.checked = false
		return err
	}
	if cache.checked && key.matches(cache.key) {
		return cache.err
	}
	cache.checked = false
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err = validate(checkCtx)
	if checkCtx.Err() != nil {
		return checkCtx.Err()
	}
	after, statErr := readNativePayloadKey(paths)
	if statErr != nil {
		return statErr
	}
	if !key.matches(after) {
		return errors.New("OpenCode native payload changed during validation")
	}
	cache.key, cache.err, cache.checked = after, err, true
	return err
}

var deliveredNativeProof nativePayloadProofCache

func validateOpenCodeNativePayload(ctx context.Context) error {
	return deliveredNativeProof.check(ctx, deliveredNativePayload, func(ctx context.Context) error {
		return validateOpenCodeNativePayloadFiles(ctx, deliveredNativePayload)
	})
}

func (paths nativePayloadPaths) change(path string, info os.FileInfo) (string, error) {
	if paths.scopedUID != 0 {
		return scopedNativePayloadChange(path, info)
	}
	return trustedNativePayloadChange(info)
}

var scopedNativeProof nativePayloadProofCache

func validateScopedOpenCodeNativePayload(ctx context.Context, uid, gid int, proof *gateway.NativeUpgradeParentProof, roots [2]string) error {
	if err := validateNativePayloadNamespace(uid, gid, proof, roots); err != nil {
		return err
	}
	paths := deliveredNativePayload
	paths.scopedUID, paths.scopedGID = uid, gid
	paths.scopedProof, paths.scopedRoots = proof, roots
	return scopedNativeProof.check(ctx, paths, func(ctx context.Context) error {
		return validateOpenCodeNativePayloadFiles(ctx, paths)
	})
}
