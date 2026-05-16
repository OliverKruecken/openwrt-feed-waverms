package hardware

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetOpenWrtVersion_ParsesRelease(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "openwrt_release")
	require.NoError(t, os.WriteFile(f, []byte(`
DISTRIB_ID="OpenWrt"
DISTRIB_RELEASE="23.05.3"
DISTRIB_REVISION="r23809-234f1a2efa"
`), 0644))

	assert.Equal(t, "23.05.3", getOpenWrtVersion(f))
}

func TestGetOpenWrtVersion_MissingFile(t *testing.T) {
	assert.Equal(t, "unknown", getOpenWrtVersion("/nonexistent/path"))
}

func TestGetOpenWrtVersion_MissingField(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "openwrt_release")
	require.NoError(t, os.WriteFile(f, []byte("DISTRIB_ID=\"OpenWrt\"\n"), 0644))
	assert.Equal(t, "unknown", getOpenWrtVersion(f))
}

func TestGetModel_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "model")
	require.NoError(t, os.WriteFile(f, []byte("GL.iNet GL-MT3000\n"), 0644))
	assert.Equal(t, "GL.iNet GL-MT3000", getModel(f))
}

func TestGetModel_MissingFile(t *testing.T) {
	assert.Equal(t, "unknown", getModel("/nonexistent/path"))
}
