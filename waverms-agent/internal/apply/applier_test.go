package apply_test

import (
	"encoding/json"
	"testing"

	"github.com/OliverKruecken/waverms-agent/internal/apply"
	"github.com/OliverKruecken/waverms-agent/internal/uci"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const wirelessMergeConfig = `{
  "wireless": {
    ".mode": "merge",
    "wifi-iface": [
      { ".name": "default_radio0", "ssid": "MyNetwork", "encryption": "psk2" }
    ]
  }
}`

func TestApply_NamedSection_SetsTypeAndOptions(t *testing.T) {
	mock := &uci.MockUCIRunner{}
	a := apply.New(mock)

	_, err := a.Apply(json.RawMessage(wirelessMergeConfig))
	require.NoError(t, err)

	assert.Contains(t, mock.Calls, "set-type wireless.default_radio0=wifi-iface")
	assert.Contains(t, mock.Calls, "set wireless.default_radio0.ssid=MyNetwork")
	assert.Contains(t, mock.Calls, "set wireless.default_radio0.encryption=psk2")
	assert.Contains(t, mock.Calls, "commit wireless")
}

func TestApply_ListOption_DeletesThenAdds(t *testing.T) {
	mock := &uci.MockUCIRunner{}
	a := apply.New(mock)

	cfg := `{
  "network": {
    ".mode": "merge",
    "interface": [
      { ".name": "lan", "proto": "static", "dns": ["8.8.8.8", "8.8.4.4"] }
    ]
  }
}`
	_, err := a.Apply(json.RawMessage(cfg))
	require.NoError(t, err)

	assert.Contains(t, mock.Calls, "delete-option network.lan.dns")
	assert.Contains(t, mock.Calls, "add_list network.lan.dns=8.8.8.8")
	assert.Contains(t, mock.Calls, "add_list network.lan.dns=8.8.4.4")
	assert.Contains(t, mock.Calls, "commit network")
}

func TestApply_ReplaceMode_DeletesUnwantedSections(t *testing.T) {
	mock := &uci.MockUCIRunner{
		Results: map[string]string{
			// extra_iface is in current config but absent from desired config.
			"show wireless": "wireless.default_radio0=wifi-iface\nwireless.extra_iface=wifi-iface\n",
		},
	}
	a := apply.New(mock)

	cfg := `{
  "wireless": {
    ".mode": "replace",
    "wifi-iface": [
      { ".name": "default_radio0", "ssid": "NewSSID" }
    ]
  }
}`
	_, err := a.Apply(json.RawMessage(cfg))
	require.NoError(t, err)

	assert.Contains(t, mock.Calls, "delete wireless.extra_iface")
	assert.NotContains(t, mock.Calls, "delete wireless.default_radio0")
	assert.Contains(t, mock.Calls, "set wireless.default_radio0.ssid=NewSSID")
}

func TestApply_ReplaceMode_KeepsDesiredNamedSection(t *testing.T) {
	mock := &uci.MockUCIRunner{
		Results: map[string]string{
			"show wireless": "wireless.default_radio0=wifi-iface\n",
		},
	}
	a := apply.New(mock)

	cfg := `{
  "wireless": {
    ".mode": "replace",
    "wifi-iface": [
      { ".name": "default_radio0", "ssid": "MySSID" }
    ]
  }
}`
	_, err := a.Apply(json.RawMessage(cfg))
	require.NoError(t, err)

	assert.NotContains(t, mock.Calls, "delete wireless.default_radio0")
	assert.Contains(t, mock.Calls, "set wireless.default_radio0.ssid=MySSID")
}

func TestApply_AnonymousSection_UsesAdd(t *testing.T) {
	mock := &uci.MockUCIRunner{}
	a := apply.New(mock)

	cfg := `{
  "firewall": {
    ".mode": "merge",
    "rule": [
      { "name": "Allow-SSH", "src": "wan", "dest_port": "22", "proto": "tcp", "target": "ACCEPT" }
    ]
  }
}`
	_, err := a.Apply(json.RawMessage(cfg))
	require.NoError(t, err)

	// Anonymous section: should call Add, not SetType.
	found := false
	for _, c := range mock.Calls {
		if c == "add firewall rule" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected 'add firewall rule' call for anonymous section")
	assert.Contains(t, mock.Calls, "commit firewall")
}

func TestApply_StagingError_RevertsAllStagedPackages(t *testing.T) {
	mock := &uci.MockUCIRunner{
		Errors: map[string]error{
			"set wireless.default_radio0.ssid=MyNetwork": assert.AnError,
		},
	}
	a := apply.New(mock)

	_, err := a.Apply(json.RawMessage(wirelessMergeConfig))
	require.Error(t, err)

	assert.Contains(t, mock.Calls, "revert wireless")
	assert.NotContains(t, mock.Calls, "commit wireless")
}

func TestApply_SkipsMetaKeys(t *testing.T) {
	mock := &uci.MockUCIRunner{}
	a := apply.New(mock)

	cfg := `{
  "packages": { "install": ["kmod-batman-adv"] },
  "reboot": false,
  "system": {
    ".mode": "merge",
    "system": [{ ".name": "system", "hostname": "router-01" }]
  }
}`
	_, err := a.Apply(json.RawMessage(cfg))
	require.NoError(t, err)

	assert.Contains(t, mock.Calls, "commit system")
	for _, call := range mock.Calls {
		assert.NotContains(t, call, "packages")
		assert.NotContains(t, call, "reboot")
	}
}

func TestApply_MultiplePackages_ReturnsAllCommitted(t *testing.T) {
	mock := &uci.MockUCIRunner{}
	a := apply.New(mock)

	cfg := `{
  "system": {
    ".mode": "merge",
    "system": [{ ".name": "system", "hostname": "r1" }]
  },
  "network": {
    ".mode": "merge",
    "interface": [{ ".name": "wan", "proto": "dhcp" }]
  }
}`
	committed, err := a.Apply(json.RawMessage(cfg))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"system", "network"}, committed)
}

func TestApply_EmptyConfig_ReturnsNoPackages(t *testing.T) {
	mock := &uci.MockUCIRunner{}
	a := apply.New(mock)

	committed, err := a.Apply(json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Empty(t, committed)
	assert.Empty(t, mock.Calls)
}

func TestApply_MergeMode_RemovesStaleOption(t *testing.T) {
	// Device has "disabled='1'" on wifinet0 but desired config does not include it.
	mock := &uci.MockUCIRunner{
		Results: map[string]string{
			"show wireless": "wireless.wifinet0=wifi-iface\nwireless.wifinet0.mode='mesh'\nwireless.wifinet0.disabled='1'\n",
		},
	}
	a := apply.New(mock)

	cfg := `{
  "wireless": {
    ".mode": "merge",
    "wifi-iface": [
      { ".name": "wifinet0", "mode": "mesh" }
    ]
  }
}`
	_, err := a.Apply(json.RawMessage(cfg))
	require.NoError(t, err)

	assert.Contains(t, mock.Calls, "delete-option wireless.wifinet0.disabled",
		"stale option 'disabled' should be removed")
	assert.NotContains(t, mock.Calls, "delete-option wireless.wifinet0.mode",
		"desired option 'mode' must not be deleted")
}

func TestApply_AnonymousSection_SkipsStaleClean(t *testing.T) {
	mock := &uci.MockUCIRunner{}
	a := apply.New(mock)

	cfg := `{
  "firewall": {
    ".mode": "merge",
    "rule": [
      { "name": "Allow-SSH", "dest_port": "22" }
    ]
  }
}`
	_, err := a.Apply(json.RawMessage(cfg))
	require.NoError(t, err)

	// cleanStaleOptions must not be called for anonymous sections (no .name).
	assert.NotContains(t, mock.Calls, "show firewall",
		"Show must not be called for stale-option cleanup on anonymous sections")
}

func TestServiceReloads_MapsKnownPackages(t *testing.T) {
	cmds := apply.ServiceReloads([]string{"wireless", "network"})
	assert.Contains(t, cmds, "wifi reload")
	assert.Contains(t, cmds, "/etc/init.d/network reload")
	assert.Len(t, cmds, 2)
}

func TestServiceReloads_UnknownPackageIgnored(t *testing.T) {
	cmds := apply.ServiceReloads([]string{"unknown-pkg"})
	assert.Empty(t, cmds)
}

func TestServiceReloads_NoDuplicates(t *testing.T) {
	cmds := apply.ServiceReloads([]string{"wireless", "wireless"})
	assert.Len(t, cmds, 1)
}
