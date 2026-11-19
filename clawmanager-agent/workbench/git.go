package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/iamlovingit/clawmanager-agent/workspacegit"
	"io"
)

type GitPayload struct {
	Root    string               `json:"root"`
	Request workspacegit.Request `json:"request"`
}
type GitEnvelope struct {
	Result *workspacegit.Response `json:"result,omitempty"`
	Error  *workspacegit.Error    `json:"error,omitempty"`
}

// RunGitHelper is called before management startup, only in the business sandbox.
func RunGitHelper(ctx context.Context, args []string, input io.Reader, output io.Writer) (bool, error) {
	if len(args) == 0 || args[0] != GitCommand {
		return false, nil
	}
	if len(args) != 1 {
		return true, Invalid()
	}
	data, err := io.ReadAll(io.LimitReader(input, MaxRequest+1))
	if err != nil || len(data) > MaxRequest {
		return true, Invalid()
	}
	var p GitPayload
	if json.Unmarshal(data, &p) != nil {
		return true, Invalid()
	}
	var result workspacegit.Response
	svc, err := workspacegit.New(p.Root)
	if err == nil {
		result, err = svc.Execute(ctx, p.Request)
	}
	envelope := GitEnvelope{Result: &result}
	if err != nil {
		envelope.Result = nil
		var safe *workspacegit.Error
		if !errors.As(err, &safe) {
			safe = &workspacegit.Error{Code: "git_failed", Message: "Git operation failed."}
		}
		envelope.Error = safe
	}
	return true, json.NewEncoder(output).Encode(envelope)
}
