package opencode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func TestOpenCodeNativeProofCacheReusesProofAndRejectsReplacedPayload(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned payload fixture requires root")
	}
	dir := t.TempDir()
	paths := nativePayloadPaths{binary: filepath.Join(dir, "native"), attestation: filepath.Join(dir, "attestation")}
	for _, path := range []string{paths.binary, paths.attestation} {
		if err := os.WriteFile(path, []byte("trusted"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var cache nativePayloadProofCache
	checks := 0
	validate := func(context.Context) error {
		checks++
		raw, err := os.ReadFile(paths.binary)
		if err != nil {
			return err
		}
		if string(raw) != "trusted" {
			return errors.New("native SHA mismatch")
		}
		return nil
	}
	for i := 0; i < 3; i++ {
		if err := cache.check(context.Background(), paths, validate); err != nil {
			t.Fatal(err)
		}
	}
	if checks != 1 {
		t.Fatalf("immutable payload was rehashed %d times", checks)
	}
	if err := os.Chmod(paths.attestation, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := cache.check(context.Background(), paths, validate); err == nil || checks != 1 {
		t.Fatalf("writable attestation entered validator/cache: checks=%d err=%v", checks, err)
	}
	if err := os.Chmod(paths.attestation, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cache.check(context.Background(), paths, validate); err != nil || checks != 2 {
		t.Fatalf("mode restoration did not revalidate: checks=%d err=%v", checks, err)
	}
	replacement := filepath.Join(dir, "replacement")
	os.WriteFile(replacement, []byte("forged!"), 0o600)
	os.Rename(replacement, paths.binary)
	if err := cache.check(context.Background(), paths, validate); err == nil || checks != 3 {
		t.Fatalf("replacement reused old proof: checks=%d err=%v", checks, err)
	}
}

func TestOpenCodeNativeProofCacheInvalidatesAfterWriteAndMtimeRestoration(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned payload fixture requires root")
	}
	dir := t.TempDir()
	paths := nativePayloadPaths{binary: filepath.Join(dir, "native"), attestation: filepath.Join(dir, "attestation")}
	for _, path := range []string{paths.binary, paths.attestation} {
		os.WriteFile(path, []byte("trusted"), 0o600)
	}
	var cache nativePayloadProofCache
	checks := 0
	validate := func(context.Context) error {
		checks++
		raw, _ := os.ReadFile(paths.binary)
		if string(raw) != "trusted" {
			return errors.New("changed native SHA")
		}
		return nil
	}
	if err := cache.check(context.Background(), paths, validate); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(paths.binary)
	time.Sleep(time.Millisecond)
	os.WriteFile(paths.binary, []byte("forged!"), 0o600)
	os.Chtimes(paths.binary, before.ModTime(), before.ModTime())
	if err := cache.check(context.Background(), paths, validate); err == nil || checks != 2 {
		t.Fatalf("changed ctime reused proof: checks=%d err=%v", checks, err)
	}
	os.Chmod(paths.binary, 0o666)
	if err := cache.check(context.Background(), paths, validate); err == nil {
		t.Fatal("business-writable native payload accepted")
	}
}

func TestOpenCodeActualPinnedNativePayloadProof(t *testing.T) {
	binary := os.Getenv("CLAWMANAGER_OPENCODE_TEST_BINARY")
	if binary == "" || os.Geteuid() != 0 {
		t.Skip("requires the observed pinned binary on the test cluster")
	}
	raw, err := os.ReadFile("upgrade_attestation.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "attestation.json")
	os.WriteFile(path, raw, 0o600)
	paths := nativePayloadPaths{binary: binary, attestation: path, python: "/usr/bin/python3"}
	var cache nativePayloadProofCache
	checks := 0
	check := func(ctx context.Context) error { checks++; return validateOpenCodeNativePayloadFiles(ctx, paths) }
	for i := 0; i < 2; i++ {
		if err := cache.check(context.Background(), paths, check); err != nil {
			t.Fatal(err)
		}
	}
	if checks != 1 {
		t.Fatal("actual pinned binary was rehashed on repeat")
	}
	originalPaths := deliveredNativePayload
	deliveredNativePayload = paths
	defer func() { deliveredNativePayload = originalPaths }()
	t.Setenv("CLAWMANAGER_OPENCODE_UPGRADE_PROTOCOL", OpenCodeUpgradeProtocol)
	metadata := NewProfile("opencode").RegistrationMetadata(gateway.Config{})
	if metadata.ProtocolVersion != OpenCodeUpgradeProtocol || len(metadata.Capabilities) != 5 || metadata.Capabilities[4] != "opencode.native-payload-poduid-v1" {
		t.Fatalf("actual verified native payload did not advertise the complete contract: %+v", metadata)
	}
	os.WriteFile(path, []byte(`{"native_version":"forged"}`), 0o600)
	if err := cache.check(context.Background(), paths, check); err == nil || checks != 2 {
		t.Fatal("forged attestation reused native proof")
	}
	metadata = NewProfile("opencode").RegistrationMetadata(gateway.Config{})
	if metadata.ProtocolVersion != "" || len(metadata.Capabilities) != 0 {
		t.Fatalf("forged payload retained native capabilities: %+v", metadata)
	}
}
