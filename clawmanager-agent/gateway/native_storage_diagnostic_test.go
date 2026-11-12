package gateway

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestNativeStorageFailureCodeContainsOnlyFixedPhaseAndCategory(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("native_storage_config: /secret/path private-token: %w", syscall.EACCES), "native_storage_config: permission denied"},
		{fmt.Errorf("native_storage_credentials: unsafe private-token"), "native_storage_credentials: unsafe configuration"},
		{errors.New("private-token arbitrary body"), "native_storage_helper: helper failed"},
	} {
		if got := NativeStorageFailureCode(tc.err); got != tc.want {
			t.Fatalf("unsafe or wrong category %q", got)
		}
	}
}
