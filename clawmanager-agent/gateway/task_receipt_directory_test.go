package gateway

import (
	"path/filepath"
	"testing"
)

func TestOpenClawReceiptDirectoryPaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "receipts")
	workspace := filepath.Join(t.TempDir(), "workspaces")
	req := CreateGatewayRequest{UserID: 7, InstanceID: 8, UID: 200008, GID: 200008, Generation: 1}
	first, err := ReceiptDirectoryPath(root, workspace, req)
	if err != nil {
		t.Fatal(err)
	}
	req.Generation = 2
	second, _ := ReceiptDirectoryPath(root, workspace, req)
	if first != second {
		t.Fatal("generation changed storage directory")
	}
	req.UserID = 9
	third, _ := ReceiptDirectoryPath(root, workspace, req)
	if third == first {
		t.Fatal("owner storage shared")
	}
	for _, bad := range []string{workspace, filepath.Join(workspace, "receipts"), filepath.Dir(workspace), "relative"} {
		if _, err := ReceiptDirectoryPath(bad, workspace, req); err == nil {
			t.Fatalf("unsafe path %s", bad)
		}
	}
	req.UID = 0
	if _, err := ReceiptDirectoryPath(root, workspace, req); err == nil {
		t.Fatal("root runtime identity accepted")
	}
}
func TestOpenClawReceiptEnvironmentUsesPoolRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "receipts")
	workspace := filepath.Join(t.TempDir(), "workspace")
	req := CreateGatewayRequest{UserID: 7, InstanceID: 8, UID: 200008, GID: 200008, Environment: map[string]string{TaskReceiptRootEnv: "/user/root", "CLAWMANAGER_TASK_RECEIPT_DIR": "/user/dir"}}
	env := OpenClawGatewayEnv([]string{TaskReceiptRootEnv + "=" + root}, Config{WorkspaceRoot: workspace}, req, workspace, 20000)
	want, _ := ReceiptDirectoryPath(root, workspace, req)
	if envValue(env, "CLAWMANAGER_TASK_RECEIPT_DIR") != want {
		t.Fatal("request changed receipt directory")
	}
	if envValue(env, TaskReceiptRootEnv) != "" {
		t.Fatal("pool root exposed to runtime")
	}
}
func TestOpenClawReceiptStorageDefaultIsUnchanged(t *testing.T) {
	t.Setenv(TaskReceiptRootEnv, "")
	if err := PrepareOpenClawReceiptDirectory(Config{}, CreateGatewayRequest{}); err != nil {
		t.Fatal(err)
	}
}
