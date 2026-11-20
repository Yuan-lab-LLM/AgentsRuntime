package receiptstorage

import (
	"path/filepath"
	"testing"
)

func TestReceiptStorageIdentityAndPathValidation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "receipts")
	data := filepath.Join(t.TempDir(), "data")
	valid := Identity{InstanceID: "8", OwnerID: "7", UID: 1000, GID: 1000}
	if err := Validate(root, valid, data); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Identity{{InstanceID: "../8", OwnerID: "7", UID: 1000, GID: 1000}, {InstanceID: "8", OwnerID: "0", UID: 1000, GID: 1000}, {InstanceID: "8", OwnerID: "7", UID: 0, GID: 1000}} {
		if Validate(root, bad, data) == nil {
			t.Fatal("invalid identity accepted")
		}
	}
	for _, bad := range []string{"relative", data, filepath.Join(data, "nested"), filepath.Dir(data)} {
		if Validate(bad, valid, data) == nil {
			t.Fatalf("unsafe root %s", bad)
		}
	}
	if string(binding(valid)) == string(binding(Identity{InstanceID: "9", OwnerID: "7", UID: 1000, GID: 1000})) {
		t.Fatal("binding not instance scoped")
	}
}
