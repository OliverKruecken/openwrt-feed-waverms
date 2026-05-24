package filewriter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOSFileWriter_WritesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")

	w := &OSFileWriter{}
	err := w.WriteFile(path, []byte("hello"), 0600)
	require.NoError(t, err)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, []byte("hello"), got)
}

func TestOSFileWriter_CreatesParentDirectories(t *testing.T) {
	// The parent directories do not exist yet — OSFileWriter must create them.
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "c", "file.txt")

	w := &OSFileWriter{}
	err := w.WriteFile(path, []byte("data"), 0600)
	require.NoError(t, err, "WriteFile should succeed even when parent dirs are missing")

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, []byte("data"), got)
}

func TestOSFileWriter_RespectsFilePermission(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")

	w := &OSFileWriter{}
	require.NoError(t, w.WriteFile(path, []byte("s3cr3t"), 0600))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}