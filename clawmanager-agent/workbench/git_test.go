package workbench

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestGitHelperBoundedEnvelope(t *testing.T) {
	raw, _ := json.Marshal(GitPayload{Root: t.TempDir()})
	var payload map[string]any
	json.Unmarshal(raw, &payload)
	payload["request"] = map[string]string{"operation": "projects"}
	raw, _ = json.Marshal(payload)
	var output bytes.Buffer
	handled, err := RunGitHelper(context.Background(), []string{GitCommand}, bytes.NewReader(raw), &output)
	if !handled || err != nil {
		t.Fatal(handled, err)
	}
	var envelope GitEnvelope
	if json.Unmarshal(output.Bytes(), &envelope) != nil || envelope.Result == nil || envelope.Error != nil {
		t.Fatal(output.String())
	}
	if _, err = RunGitHelper(context.Background(), []string{GitCommand}, strings.NewReader(strings.Repeat("x", MaxRequest+1)), &output); err == nil {
		t.Fatal("unbounded helper input")
	}
}
