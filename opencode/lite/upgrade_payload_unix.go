//go:build unix

package opencode

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"syscall"
)

func trustedNativePayloadChange(info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return "", errors.New("OpenCode native payload must be an immutable root-owned regular file")
	}
	return nativePayloadChangeTime(stat)
}

func nativePayloadChangeTime(stat *syscall.Stat_t) (string, error) {
	// ctime also invalidates a cached proof after chmod/write/mtime restoration.
	value := reflect.ValueOf(stat).Elem()
	for _, name := range []string{"Ctim", "Ctimespec"} {
		if changed := value.FieldByName(name); changed.IsValid() {
			return fmt.Sprint(changed.Interface()), nil
		}
	}
	return "", errors.New("OpenCode native payload change identity is unavailable")
}
