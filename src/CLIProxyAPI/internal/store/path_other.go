//go:build !windows

package store

func normalizeLocalPath(path string) string {
	return path
}
