package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadCredentials_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")

	require.NoError(t, SaveCredentials(path, "device-uuid-123", "supersecret", "broker.local", 8883))

	creds, err := LoadCredentials(path)
	require.NoError(t, err)

	assert.Equal(t, "device-uuid-123", creds.DeviceID)
	assert.Equal(t, "supersecret", creds.Secret)
	assert.Equal(t, "broker.local", creds.BrokerHost)
	assert.Equal(t, 8883, creds.BrokerPort)
}

func TestLoadCredentials_MinimalRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")

	require.NoError(t, SaveCredentials(path, "dev1", "sec1", "", 0))

	creds, err := LoadCredentials(path)
	require.NoError(t, err)

	assert.Equal(t, "dev1", creds.DeviceID)
	assert.Equal(t, "sec1", creds.Secret)
	assert.Empty(t, creds.BrokerHost)
	assert.Equal(t, 0, creds.BrokerPort)
}

func TestHasCredentials_True(t *testing.T) {
	creds := &Credentials{DeviceID: "abc", Secret: "xyz"}
	assert.True(t, creds.HasCredentials())
}

func TestHasCredentials_False_EmptyDeviceID(t *testing.T) {
	creds := &Credentials{Secret: "xyz"}
	assert.False(t, creds.HasCredentials())
}

func TestHasCredentials_False_EmptySecret(t *testing.T) {
	creds := &Credentials{DeviceID: "abc"}
	assert.False(t, creds.HasCredentials())
}

func TestHasCredentials_False_EmptyCreds(t *testing.T) {
	creds := &Credentials{}
	assert.False(t, creds.HasCredentials())
}

func TestHasCredentials_False_NilCreds(t *testing.T) {
	var creds *Credentials
	assert.False(t, creds.HasCredentials())
}

func TestLoadCredentials_MissingFile_ReturnsEmpty(t *testing.T) {
	creds, err := LoadCredentials("/nonexistent/credentials")
	require.NoError(t, err)
	assert.False(t, creds.HasCredentials())
}

func TestSaveCredentials_FileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")

	require.NoError(t, SaveCredentials(path, "id", "sec", "", 0))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}
