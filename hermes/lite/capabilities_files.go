package hermes

import (
	"errors"
	"io"
	"os"
	"path"
	"strings"
)

// Every ancestor and file is image-owned, not a workspace or request path.
// os.Root anchors traversal; Lstat/Open/SameFile prevents a symlink swap.
func readDesktopCapabilityFile(root *os.Root, relative string, limit int64) ([]byte, error) {
	file, err := openDesktopCapabilityFile(root, relative, limit)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("unreadable release file")
	}
	return data, nil
}

func openDesktopCapabilityFile(root *os.Root, relative string, limit int64) (*os.File, error) {
	if relative == "" || strings.HasPrefix(relative, "/") || strings.Contains(relative, "\\") || path.Clean(relative) != relative || strings.HasPrefix(relative, "../") {
		return nil, errors.New("invalid release path")
	}
	var current string
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		current = path.Join(current, part)
		info, err := root.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 || !desktopReleaseOwnerTrusted(info) {
			return nil, errors.New("untrusted release file")
		}
		if index < len(parts)-1 && !info.IsDir() {
			return nil, errors.New("invalid release directory")
		}
		if index == len(parts)-1 {
			if !info.Mode().IsRegular() || info.Size() > limit {
				return nil, errors.New("invalid release file")
			}
			file, err := root.Open(current)
			if err != nil {
				return nil, errors.New("unreadable release file")
			}
			opened, err := file.Stat()
			if err != nil || !os.SameFile(info, opened) {
				file.Close()
				return nil, errors.New("release file changed")
			}
			return file, nil
		}
	}
	return nil, errors.New("missing release file")
}
