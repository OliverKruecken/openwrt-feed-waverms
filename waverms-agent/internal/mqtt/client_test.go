package mqtt

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMockMQTTClient_Connect_RecordsOpts(t *testing.T) {
	m := NewMockMQTTClient()
	opts := ConnectOptions{
		BrokerHost: "broker.local",
		BrokerPort: 8883,
		ClientID:   "test-client",
		Username:   "user",
		Password:   "pass",
	}
	err := m.Connect(context.Background(), opts)
	require.NoError(t, err)
	require.Len(t, m.ConnectOpts, 1)
	assert.Equal(t, opts, m.ConnectOpts[0])
}

func TestMockMQTTClient_Publish_RecordsMsg(t *testing.T) {
	m := NewMockMQTTClient()
	err := m.Publish(context.Background(), "device/abc/info", []byte(`{"ok":true}`), 1, false)
	require.NoError(t, err)
	require.Len(t, m.Published, 1)
	assert.Equal(t, "device/abc/info", m.Published[0].Topic)
	assert.Equal(t, []byte(`{"ok":true}`), m.Published[0].Payload)
	assert.Equal(t, byte(1), m.Published[0].QoS)
	assert.False(t, m.Published[0].Retain)
}

func TestMockMQTTClient_Subscribe_RecordsAndDelivers(t *testing.T) {
	m := NewMockMQTTClient()

	received := make(chan []byte, 1)
	err := m.Subscribe(context.Background(), "device/abc/cmd", 1, func(topic string, payload []byte) {
		received <- payload
	})
	require.NoError(t, err)

	m.SimulateMessage("device/abc/cmd", []byte(`{"cmd_id":"x"}`))

	msg := <-received
	assert.Equal(t, []byte(`{"cmd_id":"x"}`), msg)
}

func TestMockMQTTClient_SimulateDisconnect(t *testing.T) {
	m := NewMockMQTTClient()
	ch := m.Disconnected()

	m.SimulateDisconnect()

	select {
	case <-ch:
		// expected
	default:
		t.Fatal("disconnect channel should be closed")
	}
}

func TestMockMQTTClient_SimulateDisconnect_Idempotent(t *testing.T) {
	m := NewMockMQTTClient()
	// calling twice should not panic
	m.SimulateDisconnect()
	m.SimulateDisconnect()
}

func TestMockMQTTClient_Reset_ClearsState(t *testing.T) {
	m := NewMockMQTTClient()
	_ = m.Connect(context.Background(), ConnectOptions{})
	_ = m.Publish(context.Background(), "t", []byte("p"), 0, false)
	m.SimulateDisconnect()

	m.Reset()

	assert.Empty(t, m.ConnectOpts)
	assert.Empty(t, m.Published)
	assert.Empty(t, m.Subscriptions)
	// new channel should be open
	select {
	case <-m.Disconnected():
		t.Fatal("disconnect channel should be open after Reset")
	default:
	}
}

func TestMockMQTTClient_ConnectErr(t *testing.T) {
	m := NewMockMQTTClient()
	m.SetConnectErr(assert.AnError)
	err := m.Connect(context.Background(), ConnectOptions{})
	assert.ErrorIs(t, err, assert.AnError)
}
