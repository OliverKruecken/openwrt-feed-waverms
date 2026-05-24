package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/OliverKruecken/waverms-agent/internal/config"
	"github.com/OliverKruecken/waverms-agent/internal/filewriter"
	mqttclient "github.com/OliverKruecken/waverms-agent/internal/mqtt"
	"github.com/OliverKruecken/waverms-agent/internal/uci"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestAgentWithFW(mock *mqttclient.MockMQTTClient, uciMock *uci.MockUCIRunner, fw *filewriter.MockFileWriter) *Agent {
	cfg := &config.Config{
		BrokerHost:        "broker.local",
		BrokerPort:        8883,
		HeartbeatInterval: 60,
	}
	creds := &config.Credentials{
		DeviceID: "test-device-uuid",
		Secret:   "test-secret",
	}
	return New(&Options{
		Config:     cfg,
		Creds:      creds,
		MAC:        "aa:bb:cc:dd:ee:ff",
		MQTT:       mock,
		UCI:        uciMock,
		FileWriter: fw,
		Version:    "1.0.0",
	})
}

func newTestAgent(mock *mqttclient.MockMQTTClient, uciMock *uci.MockUCIRunner) *Agent {
	return newTestAgentWithFW(mock, uciMock, &filewriter.MockFileWriter{})
}

func TestPublishInfo_SendsToCorrectTopic(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	a := newTestAgent(mock, &uci.MockUCIRunner{})

	err := a.publishInfo(context.Background())
	require.NoError(t, err)

	require.Len(t, mock.Published, 1)
	assert.Equal(t, "device/test-device-uuid/info", mock.Published[0].Topic)
}

func TestPublishInfo_PayloadContainsDeviceID(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	a := newTestAgent(mock, &uci.MockUCIRunner{})

	err := a.publishInfo(context.Background())
	require.NoError(t, err)

	var info InfoPayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &info))
	assert.Equal(t, "test-device-uuid", info.DeviceID)
	assert.Equal(t, "1.0.0", info.AgentVersion)
	assert.NotEmpty(t, info.Timestamp)
}

func TestPublishInfo_PayloadContainsCapabilities(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	a := newTestAgent(mock, &uci.MockUCIRunner{})

	err := a.publishInfo(context.Background())
	require.NoError(t, err)

	var info InfoPayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &info))
	assert.NotEmpty(t, info.Capabilities, "capabilities must be non-empty")
}

func TestPublishInfo_CapabilitiesContainExpectedTypes(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	a := newTestAgent(mock, &uci.MockUCIRunner{})

	err := a.publishInfo(context.Background())
	require.NoError(t, err)

	var info InfoPayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &info))
	assert.Contains(t, info.Capabilities, "uci_set")
	assert.Contains(t, info.Capabilities, "config_apply")
	assert.Contains(t, info.Capabilities, "file_write")
	assert.Contains(t, info.Capabilities, "state_report")
	assert.Contains(t, info.Capabilities, "host_key_restore")
}

func TestHandleCommand_HostKeyRestore_WritesFilesAndRestartDropbear(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	uciMock := &uci.MockUCIRunner{}
	fw := &filewriter.MockFileWriter{}
	a := newTestAgentWithFW(mock, uciMock, fw)

	payload := `{
		"cmd_id": "hkr-1",
		"type": "host_key_restore",
		"payload": {
			"keys": {
				"ssh_host_ed25519_key":     "-----BEGIN OPENSSH PRIVATE KEY-----\nfake_ed25519\n",
				"ssh_host_ed25519_key.pub": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA fake"
			}
		}
	}`

	a.handleCommand("device/test-device-uuid/cmd", []byte(payload))

	// Both key files must have been written under /etc/dropbear/
	paths := make(map[string]bool)
	for _, c := range fw.Calls {
		paths[c.Path] = true
		assert.Equal(t, os.FileMode(0600), c.Perm, "host key files must have mode 0600")
	}
	assert.True(t, paths["/etc/dropbear/ssh_host_ed25519_key"], "private key must be written")
	assert.True(t, paths["/etc/dropbear/ssh_host_ed25519_key.pub"], "public key must be written")

	// Dropbear must have been restarted
	assert.Contains(t, uciMock.Calls, "raw /etc/init.d/dropbear restart")

	// Ack must be "ok"
	require.Len(t, mock.Published, 1)
	var ack AckPayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &ack))
	assert.Equal(t, "hkr-1", ack.CmdID)
	assert.Equal(t, "ok", ack.Status)
}

func TestHandleCommand_HostKeyRestore_PathTraversalGuard(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	uciMock := &uci.MockUCIRunner{}
	fw := &filewriter.MockFileWriter{}
	a := newTestAgentWithFW(mock, uciMock, fw)

	// A key with a path-traversal filename must be stripped to its basename.
	payload := `{
		"cmd_id": "hkr-traverse",
		"type": "host_key_restore",
		"payload": {
			"keys": {
				"../../etc/passwd": "evil content"
			}
		}
	}`

	a.handleCommand("device/test-device-uuid/cmd", []byte(payload))

	// File must be written as /etc/dropbear/passwd, not /etc/passwd.
	require.Len(t, fw.Calls, 1)
	assert.Equal(t, "/etc/dropbear/passwd", fw.Calls[0].Path)
}

func TestHandleCommand_HostKeyRestore_WriteError_AcksError(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	uciMock := &uci.MockUCIRunner{}
	fw := &filewriter.MockFileWriter{
		Errors: map[string]error{
			"/etc/dropbear/ssh_host_ed25519_key": errors.New("disk full"),
		},
	}
	a := newTestAgentWithFW(mock, uciMock, fw)

	payload := `{
		"cmd_id": "hkr-err",
		"type": "host_key_restore",
		"payload": {
			"keys": {"ssh_host_ed25519_key": "content"}
		}
	}`

	a.handleCommand("device/test-device-uuid/cmd", []byte(payload))

	require.Len(t, mock.Published, 1)
	var ack AckPayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &ack))
	assert.Equal(t, "error", ack.Status)
}

func TestHandleCommand_HostKeyRestore_EmptyKeys_AcksError(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	a := newTestAgent(mock, &uci.MockUCIRunner{})

	payload := `{"cmd_id": "hkr-empty", "type": "host_key_restore", "payload": {"keys": {}}}`
	a.handleCommand("device/test-device-uuid/cmd", []byte(payload))

	require.Len(t, mock.Published, 1)
	var ack AckPayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &ack))
	assert.Equal(t, "error", ack.Status)
}

func TestHandleCommand_HostKeyRestore_DropbearRestartFailure_StillAcksOk(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	uciMock := &uci.MockUCIRunner{
		Errors: map[string]error{
			"raw /etc/init.d/dropbear restart": errors.New("service not found"),
		},
	}
	fw := &filewriter.MockFileWriter{}
	a := newTestAgentWithFW(mock, uciMock, fw)

	payload := `{
		"cmd_id": "hkr-restart-fail",
		"type": "host_key_restore",
		"payload": {"keys": {"ssh_host_ed25519_key": "content"}}
	}`

	a.handleCommand("device/test-device-uuid/cmd", []byte(payload))

	// Dropbear restart failure is non-fatal — ack must still be "ok"
	require.Len(t, mock.Published, 1)
	var ack AckPayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &ack))
	assert.Equal(t, "ok", ack.Status)
}

func TestHandleCommand_UCISet_ExecutesAndAcks(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	uciMock := &uci.MockUCIRunner{}
	a := newTestAgent(mock, uciMock)

	payload := `{
		"cmd_id": "cmd-abc",
		"type": "uci_set",
		"payload": {"commands": ["uci set system.@system[0].hostname=router-01", "uci commit system"]}
	}`

	a.handleCommand("device/test-device-uuid/cmd", []byte(payload))

	// UCI must have been called
	assert.Contains(t, uciMock.Calls, "raw set system.@system[0].hostname=router-01")
	assert.Contains(t, uciMock.Calls, "raw commit system")

	// Ack must be published
	require.Len(t, mock.Published, 1)
	assert.Equal(t, "device/test-device-uuid/ack", mock.Published[0].Topic)

	var ack AckPayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &ack))
	assert.Equal(t, "cmd-abc", ack.CmdID)
	assert.Equal(t, "ok", ack.Status)
}

func TestHandleCommand_UCISet_ErrorReflectedInAck(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	uciMock := &uci.MockUCIRunner{
		Errors: map[string]error{
			"raw commit system": assert.AnError,
		},
	}
	a := newTestAgent(mock, uciMock)

	payload := `{
		"cmd_id": "cmd-err",
		"type": "uci_set",
		"payload": {"commands": ["uci commit system"]}
	}`

	a.handleCommand("device/test-device-uuid/cmd", []byte(payload))

	require.Len(t, mock.Published, 1)
	var ack AckPayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &ack))
	assert.Equal(t, "cmd-err", ack.CmdID)
	assert.Equal(t, "error", ack.Status)
}

func TestHandleCommand_UnknownType_ErrorAck(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	a := newTestAgent(mock, &uci.MockUCIRunner{})

	payload := `{"cmd_id": "cmd-x", "type": "unknown_type", "payload": {}}`
	a.handleCommand("device/test-device-uuid/cmd", []byte(payload))

	require.Len(t, mock.Published, 1)
	var ack AckPayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &ack))
	assert.Equal(t, "error", ack.Status)
}

func TestHandleCommand_InvalidJSON_NoPanic(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	a := newTestAgent(mock, &uci.MockUCIRunner{})

	// Should not panic, should not publish anything
	a.handleCommand("device/test-device-uuid/cmd", []byte(`not-json`))
	assert.Empty(t, mock.Published)
}

func TestRunSession_SetsLWTOnConnect(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	a := newTestAgent(mock, &uci.MockUCIRunner{})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		a.runSession(ctx) //nolint:errcheck
	}()

	// Give the session a moment to connect.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runSession did not stop on ctx cancel")
	}

	require.Len(t, mock.ConnectOpts, 1)
	connOpts := mock.ConnectOpts[0]
	require.NotNil(t, connOpts.LWT)
	assert.Equal(t, "device/test-device-uuid/status", connOpts.LWT.Topic)
	assert.Equal(t, []byte("offline"), connOpts.LWT.Payload)
	assert.Equal(t, byte(1), connOpts.LWT.QoS)
	assert.True(t, connOpts.LWT.Retain)
}

func TestRunSession_ReconnectsAfterDisconnect(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	a := newTestAgent(mock, &uci.MockUCIRunner{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		a.Run(ctx) //nolint:errcheck
	}()

	// Wait for first session to start.
	time.Sleep(30 * time.Millisecond)

	// Trigger a disconnect.
	mock.SimulateDisconnect()

	// The reconnect loop waits 1 second before the second connect attempt.
	// Wait long enough for the reconnect, with a generous margin.
	time.Sleep(1500 * time.Millisecond)

	cancel()
	<-done

	// Should have connected at least twice: initial + reconnect.
	connectCount := mock.ConnectCount()
	assert.GreaterOrEqual(t, connectCount, 2, "expected at least 2 connect calls (initial + reconnect)")
}

func TestRun_SkipsBootstrapWhenCredsPresent(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	a := newTestAgent(mock, &uci.MockUCIRunner{})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- a.Run(ctx)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done

	// With credentials present, Connect should be called with device credentials (not bootstrap).
	require.Len(t, mock.ConnectOpts, 1)
	assert.Equal(t, "test-device-uuid", mock.ConnectOpts[0].Username)
}

func TestReadHostname_ReturnsUnknownOnMissingFile(t *testing.T) {
	h := readHostname()
	// Either the real hostname or "unknown" – both are acceptable in test env.
	assert.NotEmpty(t, h)
}

func TestReadUptimeSeconds_ReturnsNonNegative(t *testing.T) {
	u := readUptimeSeconds()
	assert.GreaterOrEqual(t, u, int64(0))
}

func TestHandleCommand_StripUCIPrefix(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	uciMock := &uci.MockUCIRunner{}
	a := newTestAgent(mock, uciMock)

	payload := `{
		"cmd_id": "cmd-strip",
		"type": "uci_set",
		"payload": {"commands": ["uci set network.wan.proto=dhcp"]}
	}`
	a.handleCommand("device/test-device-uuid/cmd", []byte(payload))

	assert.Contains(t, uciMock.Calls, "raw set network.wan.proto=dhcp")
}

// systemUCIOutput is minimal valid uci export output for the system package.
const systemUCIOutput = `package system

config system
	option hostname 'OpenWrt'
`

func TestHandleStateRequest_PublishesStateToCorrectTopic(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	uciMock := &uci.MockUCIRunner{
		Results: map[string]string{
			"export system": systemUCIOutput,
		},
	}
	a := newTestAgent(mock, uciMock)

	a.handleStateRequest("device/test-device-uuid/state/request", []byte(`{"packages":["system"]}`))

	require.Len(t, mock.Published, 1)
	assert.Equal(t, "device/test-device-uuid/state", mock.Published[0].Topic)
}

func TestHandleStateRequest_TriggerIsRequest(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	uciMock := &uci.MockUCIRunner{
		Results: map[string]string{
			"export system": systemUCIOutput,
		},
	}
	a := newTestAgent(mock, uciMock)

	a.handleStateRequest("device/test-device-uuid/state/request", []byte(`{"packages":["system"]}`))

	require.Len(t, mock.Published, 1)
	var state StatePayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &state))
	assert.Equal(t, "request", state.Trigger)
}

func TestHandleStateRequest_IncludesRequestedPackage(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	uciMock := &uci.MockUCIRunner{
		Results: map[string]string{
			"export system": systemUCIOutput,
		},
	}
	a := newTestAgent(mock, uciMock)

	a.handleStateRequest("device/test-device-uuid/state/request", []byte(`{"packages":["system"]}`))

	require.Len(t, mock.Published, 1)
	var state StatePayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &state))
	_, ok := state.Packages["system"]
	assert.True(t, ok, "system package should be present in state payload")
}

func TestHandleStateRequest_SkipsMissingPackage(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	uciMock := &uci.MockUCIRunner{
		Results: map[string]string{
			"export system": systemUCIOutput,
		},
		Errors: map[string]error{
			"export wireless": assert.AnError,
		},
	}
	a := newTestAgent(mock, uciMock)

	a.handleStateRequest("device/test-device-uuid/state/request", []byte(`{"packages":["system","wireless"]}`))

	require.Len(t, mock.Published, 1)
	var state StatePayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &state))
	_, hasSystem := state.Packages["system"]
	_, hasWireless := state.Packages["wireless"]
	assert.True(t, hasSystem, "system should be present")
	assert.False(t, hasWireless, "wireless failed to export and must be absent")
}

func TestHandleStateRequest_EmptyPackagesList_UsesDefaultPackages(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	// Only inject system; all others will silently return empty string → no sections → skipped.
	uciMock := &uci.MockUCIRunner{
		Results: map[string]string{
			"export system": systemUCIOutput,
		},
	}
	a := newTestAgent(mock, uciMock)

	a.handleStateRequest("device/test-device-uuid/state/request", []byte(`{"packages":[]}`))

	require.Len(t, mock.Published, 1)
	var state StatePayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &state))
	_, ok := state.Packages["system"]
	assert.True(t, ok, "system should be in Packages when empty list triggers default set")
}

func TestHandleCommand_ConfigApply_SuccessAcksAndPublishesState(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	uciMock := &uci.MockUCIRunner{
		Results: map[string]string{
			"export system": systemUCIOutput,
		},
	}
	a := newTestAgent(mock, uciMock)

	payload := `{
		"cmd_id": "apply-ok",
		"type": "config_apply",
		"payload": {
			"system": {
				".mode": "merge",
				"system": [{ ".name": "system", "hostname": "router-01" }]
			}
		}
	}`
	a.handleCommand("device/test-device-uuid/cmd", []byte(payload))

	// Should publish ack + state (apply_success).
	require.Len(t, mock.Published, 2)

	topics := []string{mock.Published[0].Topic, mock.Published[1].Topic}
	assert.Contains(t, topics, "device/test-device-uuid/ack")
	assert.Contains(t, topics, "device/test-device-uuid/state")

	// Find the ack message.
	var ack AckPayload
	for _, pub := range mock.Published {
		if pub.Topic == "device/test-device-uuid/ack" {
			require.NoError(t, json.Unmarshal(pub.Payload, &ack))
		}
	}
	assert.Equal(t, "apply-ok", ack.CmdID)
	assert.Equal(t, "ok", ack.Status)

	// UCI staging + commit should have been called.
	assert.Contains(t, uciMock.Calls, "set-type system.system=system")
	assert.Contains(t, uciMock.Calls, "set system.system.hostname=router-01")
	assert.Contains(t, uciMock.Calls, "commit system")
}

func TestHandleCommand_ConfigApply_StagingError_AcksError(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	uciMock := &uci.MockUCIRunner{
		Errors: map[string]error{
			"set system.system.hostname=router-01": assert.AnError,
		},
	}
	a := newTestAgent(mock, uciMock)

	payload := `{
		"cmd_id": "apply-err",
		"type": "config_apply",
		"payload": {
			"system": {
				".mode": "merge",
				"system": [{ ".name": "system", "hostname": "router-01" }]
			}
		}
	}`
	a.handleCommand("device/test-device-uuid/cmd", []byte(payload))

	// Only ack published (no state on error).
	require.Len(t, mock.Published, 1)
	var ack AckPayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &ack))
	assert.Equal(t, "apply-err", ack.CmdID)
	assert.Equal(t, "error", ack.Status)
	assert.NotEmpty(t, ack.Output)

	// Revert must have been called.
	assert.Contains(t, uciMock.Calls, "revert system")
}

func TestHandleCommand_ConfigApply_StateTriggerIsApplySuccess(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	uciMock := &uci.MockUCIRunner{
		Results: map[string]string{
			"export system": systemUCIOutput,
		},
	}
	a := newTestAgent(mock, uciMock)

	payload := `{
		"cmd_id": "apply-trigger",
		"type": "config_apply",
		"payload": {
			"system": {
				".mode": "merge",
				"system": [{ ".name": "system", "hostname": "test-host" }]
			}
		}
	}`
	a.handleCommand("device/test-device-uuid/cmd", []byte(payload))

	// Find the state message and verify trigger.
	for _, pub := range mock.Published {
		if pub.Topic == "device/test-device-uuid/state" {
			var state StatePayload
			require.NoError(t, json.Unmarshal(pub.Payload, &state))
			assert.Equal(t, "apply_success", state.Trigger)
			return
		}
	}
	t.Fatal("state message not published")
}

func TestHandleStateRequest_InvalidJSON_NoPanic(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	a := newTestAgent(mock, &uci.MockUCIRunner{})

	a.handleStateRequest("device/test-device-uuid/state/request", []byte(`not-json`))

	assert.Empty(t, mock.Published, "no publish should occur on invalid JSON")
}

func TestPublishState_ConnectTrigger(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	uciMock := &uci.MockUCIRunner{
		Results: map[string]string{
			"export system": systemUCIOutput,
		},
	}
	a := newTestAgent(mock, uciMock)

	err := a.publishState(context.Background(), "connect", []string{"system"})
	require.NoError(t, err)

	require.Len(t, mock.Published, 1)
	var state StatePayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &state))
	assert.Equal(t, "connect", state.Trigger)
	assert.NotEmpty(t, state.Timestamp)
}

func TestRunSession_SubscribesStateRequest(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	a := newTestAgent(mock, &uci.MockUCIRunner{})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		a.runSession(ctx) //nolint:errcheck
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runSession did not stop on ctx cancel")
	}

	assert.True(t, mock.HasSubscription("device/test-device-uuid/state/request"),
		"runSession must subscribe to device/{id}/state/request")
}

// ---- file_write command tests -----------------------------------------------

func TestHandleCommand_FileWrite_WritesFileAndAcksOk(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	fw := &filewriter.MockFileWriter{}
	a := newTestAgentWithFW(mock, &uci.MockUCIRunner{}, fw)

	payload := `{
		"cmd_id": "fw-ok",
		"type": "file_write",
		"payload": {"path": "/etc/dropbear/authorized_keys", "content": "ssh-ed25519 AAAA...", "mode": "0600"}
	}`
	a.handleCommand("device/test-device-uuid/cmd", []byte(payload))

	// Verify file write was recorded.
	require.Len(t, fw.Calls, 1)
	assert.Equal(t, "/etc/dropbear/authorized_keys", fw.Calls[0].Path)
	assert.Equal(t, []byte("ssh-ed25519 AAAA..."), fw.Calls[0].Content)
	assert.Equal(t, os.FileMode(0600), fw.Calls[0].Perm)

	// Verify ack.
	require.Len(t, mock.Published, 1)
	assert.Equal(t, "device/test-device-uuid/ack", mock.Published[0].Topic)
	var ack AckPayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &ack))
	assert.Equal(t, "fw-ok", ack.CmdID)
	assert.Equal(t, "ok", ack.Status)
}

func TestHandleCommand_FileWrite_WriteError_AcksError(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	fw := &filewriter.MockFileWriter{
		Errors: map[string]error{
			"/etc/dropbear/authorized_keys": errors.New("permission denied"),
		},
	}
	a := newTestAgentWithFW(mock, &uci.MockUCIRunner{}, fw)

	payload := `{
		"cmd_id": "fw-err",
		"type": "file_write",
		"payload": {"path": "/etc/dropbear/authorized_keys", "content": "key data", "mode": "0600"}
	}`
	a.handleCommand("device/test-device-uuid/cmd", []byte(payload))

	require.Len(t, mock.Published, 1)
	var ack AckPayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &ack))
	assert.Equal(t, "fw-err", ack.CmdID)
	assert.Equal(t, "error", ack.Status)
	assert.Contains(t, ack.Output, "permission denied")
}

func TestHandleCommand_FileWrite_InvalidPayload_AcksError(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	fw := &filewriter.MockFileWriter{}
	a := newTestAgentWithFW(mock, &uci.MockUCIRunner{}, fw)

	payload := `{"cmd_id": "fw-bad", "type": "file_write", "payload": "not-an-object"}`
	a.handleCommand("device/test-device-uuid/cmd", []byte(payload))

	require.Len(t, mock.Published, 1)
	var ack AckPayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &ack))
	assert.Equal(t, "fw-bad", ack.CmdID)
	assert.Equal(t, "error", ack.Status)
	assert.Contains(t, ack.Output, "invalid payload")

	// WriteFile must not have been called.
	assert.Empty(t, fw.Calls)
}

func TestHandleCommand_FileWrite_MissingPath_AcksError(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	fw := &filewriter.MockFileWriter{}
	a := newTestAgentWithFW(mock, &uci.MockUCIRunner{}, fw)

	payload := `{"cmd_id": "fw-nopath", "type": "file_write", "payload": {"path": "", "content": "data"}}`
	a.handleCommand("device/test-device-uuid/cmd", []byte(payload))

	require.Len(t, mock.Published, 1)
	var ack AckPayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &ack))
	assert.Equal(t, "fw-nopath", ack.CmdID)
	assert.Equal(t, "error", ack.Status)
	assert.Contains(t, ack.Output, "path is required")

	assert.Empty(t, fw.Calls)
}

func TestHandleCommand_FileWrite_CustomMode_UsesCorrectPerm(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	fw := &filewriter.MockFileWriter{}
	a := newTestAgentWithFW(mock, &uci.MockUCIRunner{}, fw)

	payload := `{
		"cmd_id": "fw-mode",
		"type": "file_write",
		"payload": {"path": "/tmp/test.txt", "content": "hello", "mode": "0644"}
	}`
	a.handleCommand("device/test-device-uuid/cmd", []byte(payload))

	require.Len(t, fw.Calls, 1)
	assert.Equal(t, os.FileMode(0644), fw.Calls[0].Perm)

	require.Len(t, mock.Published, 1)
	var ack AckPayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &ack))
	assert.Equal(t, "ok", ack.Status)
}

func TestHandleCommand_FileWrite_NoMode_Defaults0600(t *testing.T) {
	mock := mqttclient.NewMockMQTTClient()
	fw := &filewriter.MockFileWriter{}
	a := newTestAgentWithFW(mock, &uci.MockUCIRunner{}, fw)

	// Omit "mode" field entirely.
	payload := `{
		"cmd_id": "fw-nomode",
		"type": "file_write",
		"payload": {"path": "/tmp/nomode.txt", "content": "data"}
	}`
	a.handleCommand("device/test-device-uuid/cmd", []byte(payload))

	require.Len(t, fw.Calls, 1)
	assert.Equal(t, os.FileMode(0600), fw.Calls[0].Perm)

	require.Len(t, mock.Published, 1)
	var ack AckPayload
	require.NoError(t, json.Unmarshal(mock.Published[0].Payload, &ack))
	assert.Equal(t, "ok", ack.Status)
}
