//go:build linux

package openclaw

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

// This opt-in test uses only its fresh workspace and installed image-local
// OpenClaw. It exercises the actual maintenance launcher and complete public
// compatibility preflight, rather than a substitute offline shell launcher.
func TestNativeOfflineMaintenanceAcceptance(t *testing.T) {
	if os.Getenv("CLAWMANAGER_NATIVE_MAINTENANCE_ACCEPTANCE") != "1" {
		t.Skip("requires disposable native maintenance image")
	}
	cfg, create, _, _ := nativeAcceptanceSetup(t, "openclaw")
	var installed struct {
		Version string `json:"version"`
	}
	data, err := os.ReadFile("/usr/local/lib/node_modules/openclaw/package.json")
	if err != nil || json.Unmarshal(data, &installed) != nil || installed.Version != "2026.10.2" {
		t.Fatal("requires installed pinned OpenClaw 2026.10.2")
	}
	cfg.Runtime = NewProfile("openclaw")
	cfg.OpenClawVersion = installed.Version
	cfg.PodUID = "disposable-maintenance-pod"
	cfg.ProcessStopTimeout = 2 * time.Second
	workspace := create.WorkspacePath
	config := []byte(`{"gateway":{"mode":"local","auth":{"mode":"token","token":"fake-maintenance-local-only"}},"plugins":{"allow":[]}}`)
	configPath := filepath.Join(workspace, "home/.openclaw/openclaw.json")
	nativeAcceptanceWrite(t, workspace, "home/.openclaw/openclaw.json", config)
	manager := gateway.NewGatewayManager(cfg, nil, nil)
	req := WorkspaceUpgradeRequest{InstanceID: create.InstanceID, UserID: create.UserID, Generation: create.Generation, UID: create.UID, GID: create.GID, RolloutID: "offline-native"}
	service := &upgradeService{cfg: cfg, host: manager}
	closeIO, err := service.beginIO(req, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeIO != nil {
			closeIO()
		}
	}()
	outer, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	positive, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal("outer localhost positive control unavailable")
	}
	positive.Close()
	const script = `const fs=require('fs'),net=require('net');
const sock=net.connect(Number(process.env.CM_MAINTENANCE_PARENT_PORT),'127.0.0.1');
let ended=false;function done(reachable){if(ended)return;ended=true;sock.destroy();
console.log(JSON.stringify({uid:process.getuid(),net:fs.readlinkSync('/proc/self/ns/net'),reachable,
grant:Object.keys(process.env).some(k=>/^RUNTIME_AGENT_(CONTROL|REPORT)_TOKEN$|CAPABILITY|LEASE_TOKEN/.test(k))}));}
sock.setTimeout(500,()=>done(false));sock.once('connect',()=>done(true));sock.once('error',()=>done(false));`
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.Command("/usr/local/bin/node", "-e", script)
	cmd.Dir = workspace
	cmd.Env = service.upgradeCommandEnv(map[string]string{"HOME": filepath.Join(workspace, "home"), "CM_MAINTENANCE_PARENT_PORT": strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)})
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := service.runUpgradeCommand(withReadOnlyUpgradeCommands(ctx), cmd, req); err != nil {
		t.Fatal("actual maintenance launcher failed", err)
	}
	var observed struct {
		UID       int    `json:"uid"`
		Net       string `json:"net"`
		Reachable bool   `json:"reachable"`
		Grant     bool   `json:"grant"`
	}
	if json.Unmarshal(output.Bytes(), &observed) != nil || observed.UID != create.UID || observed.Net == "" || observed.Net == outer || observed.Reachable || observed.Grant {
		t.Fatal("maintenance execution did not preserve disconnected nonroot scope")
	}
	if service.io.commandUncertain {
		t.Fatal("tracked native probe teardown was not confirmed")
	}
	closeIO()
	closeIO = nil
	result, err := manager.PreflightUpgradeCompatibility(ctx, req)
	if err != nil {
		t.Fatal("actual pinned native preflight failed", err)
	}
	if result.Status != "compatible" || !result.DoctorValidated || !result.ConfigValidated || !result.SessionDryRunValid {
		t.Fatal("native preflight omitted Doctor/config/session dry-run")
	}
	after, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(after, config) {
		t.Fatal("readonly live configuration changed")
	}
	entries, err := os.ReadDir(filepath.Join(manager.UpgradeRoot(), "rollout-"+req.RolloutID, "instance-42"))
	if err != nil || len(entries) != 0 {
		t.Fatal("confirmed native preflight retained an operation capsule")
	}
	t.Log("actual protected maintenance launcher isolated nonroot NET, denied reachable outer localhost, confirmed Tracker teardown; installed Doctor/config/session dry-run passed with unchanged live config")
}
