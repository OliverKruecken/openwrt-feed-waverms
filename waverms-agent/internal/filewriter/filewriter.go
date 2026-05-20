// Package filewriter provides the FileWriter interface and implementations for
// writing files to the device filesystem.
package filewriter

import "os"

// FileWriter abstracts filesystem writes so that command handlers can be
// tested without touching the real filesystem.
type FileWriter interface {
	WriteFile(path string, content []byte, perm os.FileMode) error
}

// OSFileWriter is the production implementation backed by os.WriteFile.
type OSFileWriter struct{}

// WriteFile writes content to path with the given permission bits.
func (w *OSFileWriter) WriteFile(path string, content []byte, perm os.FileMode) error {
	return os.WriteFile(path, content, perm)
}
