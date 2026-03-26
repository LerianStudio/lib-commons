package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultAllowedCertDirs are the directories where TLS certificate files
// are allowed to be read from.  Callers can provide additional directories.
//
// The list includes the OS temporary directory (os.TempDir()) to support
// platforms where the default temp path differs from /tmp (e.g., macOS
// uses /var/folders/... which symlink-resolves to /private/var/folders/...).
var DefaultAllowedCertDirs = defaultAllowedCertDirs()

func defaultAllowedCertDirs() []string {
	dirs := []string{
		"/etc/ssl/",
		"/etc/pki/",
		"/run/secrets/",
		"/var/run/secrets/",
		"/tmp/",
	}

	// Add the platform-specific temp directory if it differs from /tmp.
	// On macOS, os.TempDir() returns a path under /var/folders/ which is
	// not covered by the /tmp/ entry above.
	if tmpDir := os.TempDir(); tmpDir != "" && tmpDir != "/tmp" {
		if !strings.HasSuffix(tmpDir, "/") {
			tmpDir += "/"
		}

		dirs = append(dirs, tmpDir)
	}

	return dirs
}

// ValidateCertPath ensures that path resolves to a file inside one of the
// allowed directories.  This prevents path-traversal attacks when certificate
// paths originate from external configuration (e.g., Tenant Manager API).
func ValidateCertPath(path string, extraAllowedDirs ...string) error {
	if path == "" {
		return nil // empty path means "not configured"
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid certificate path %q: %w", path, err)
	}

	// Resolve symlinks
	resolved, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("cannot resolve certificate path %q: %w", path, err)
		}

		resolved = absPath // file may not exist yet (pre-provisioned path)
	}

	allowed := append(DefaultAllowedCertDirs, extraAllowedDirs...) //nolint:gocritic // append to copy is intentional
	for _, dir := range allowed {
		if strings.HasPrefix(resolved, dir) {
			return nil
		}

		// Also resolve symlinks on the allowed directory itself, so that
		// e.g. /tmp/ (→ /private/tmp/ on macOS) matches resolved paths.
		if resolvedDir, err := filepath.EvalSymlinks(dir); err == nil && resolvedDir != dir {
			if !strings.HasSuffix(resolvedDir, "/") {
				resolvedDir += "/"
			}

			if strings.HasPrefix(resolved, resolvedDir) {
				return nil
			}
		}
	}

	return fmt.Errorf("certificate path %q resolves to %q which is outside allowed directories %v", path, resolved, allowed)
}
