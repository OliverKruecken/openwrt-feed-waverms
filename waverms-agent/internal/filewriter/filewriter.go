// Package filewriter provides the FileWriter interface and implementations for
// writing files to the device filesystem.
package filewriter

import (
	"fmt"
	"os"
	"path/filepath"
)

// FileWriter abstracts filesystem writes so that command handlers can be
// tested without touching the real filesystem.
type FileWriter interface {
	WriteFile(path string, content []byte, perm os.FileMode) error
}

// OSFileWriter is the production implementation backed by os.WriteFile.
type OSFileWriter struct{}

// WriteFile creates all parent directories (with mode 0755) and then writes
// content to path with the given permission bits. This ensures that paths like
// /etc/dropbear/authorized_keys succeed even when the parent directory does not
// yet exist on the device.
func (w *OSFileWriter) WriteFile(path string, content []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return os.WriteFile(path, content, perm)
}
