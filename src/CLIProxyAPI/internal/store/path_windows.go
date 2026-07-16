package store

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func normalizeLocalPath(path string) string {
	if path == "" {
		return path
	}
	clean := filepath.Clean(path)
	abs, err := filepath.Abs(clean)
	if err == nil {
		clean = abs
	}

	volume := filepath.VolumeName(clean)
	rest := strings.TrimPrefix(clean, volume)
	if rest == "" {
		return expandLongPath(clean)
	}
	rest = strings.TrimLeft(rest, `\/`)
	parts := strings.FieldsFunc(rest, func(r rune) bool {
		return r == '\\' || r == '/'
	})

	prefix := volume
	if prefix == "" && filepath.IsAbs(clean) {
		prefix = string(os.PathSeparator)
	} else if prefix != "" {
		prefix += string(os.PathSeparator)
	}

	current := strings.TrimRight(prefix, `\/`)
	if current == "" {
		current = prefix
	}
	for i, part := range parts {
		candidate := filepath.Join(current, part)
		if _, statErr := os.Stat(candidate); statErr != nil {
			remaining := append([]string{candidate}, parts[i+1:]...)
			return filepath.Join(remaining...)
		}
		current = expandLongPath(candidate)
	}
	return current
}

func expandLongPath(path string) string {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return path
	}
	n, err := windows.GetLongPathName(ptr, nil, 0)
	if err != nil || n == 0 {
		return path
	}
	buf := make([]uint16, n)
	n, err = windows.GetLongPathName(ptr, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 {
		return path
	}
	return windows.UTF16ToString(buf[:n])
}
