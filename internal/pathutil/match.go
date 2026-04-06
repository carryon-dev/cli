package pathutil

import (
	"path/filepath"
	"strings"
)

// IsUnderPath reports whether child is at or under parent in the filesystem
// hierarchy. It is path-boundary aware: /a/bc is NOT under /a/b.
// Both paths are cleaned before comparison. Returns false if either is empty.
func IsUnderPath(child, parent string) bool {
	if child == "" || parent == "" {
		return false
	}
	child = filepath.Clean(child)
	parent = filepath.Clean(parent)
	if child == parent {
		return true
	}
	// Ensure parent ends with separator for prefix check.
	// filepath.Clean("/") already ends with a separator, so avoid doubling it.
	sep := string(filepath.Separator)
	if strings.HasSuffix(parent, sep) {
		return strings.HasPrefix(child, parent)
	}
	return strings.HasPrefix(child, parent+sep)
}
