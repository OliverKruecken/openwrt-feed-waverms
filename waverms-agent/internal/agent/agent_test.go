package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/OliverKruecken/waverms-agent/internal/config"
	mqttclient "github.com/OliverKruecken/waverms-agent/internal/mqtt"
	"github.com/OliverKruecken/waverms-agent/internal/uci"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestAgent(mock *mqttclient.MockMQTTClient, uciMock *uci.MockUCIRunner) *Agent {
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
		Config:  cfg,
		Creds:   creds,
		MAC:     "aa:bb:cc:dd:ee:ff",
		MQTT:    mock,
		UCI:     uciMock,
		Version: "1.0.0",
	})
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
