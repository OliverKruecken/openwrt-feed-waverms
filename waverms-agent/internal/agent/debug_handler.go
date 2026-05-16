package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	mqttclient "github.com/OliverKruecken/waverms-agent/internal/mqtt"
)

// debugState holds the runtime toggle flag, shared across all WithAttrs/WithGroup clones.
type debugState struct {
	mu      sync.RWMutex
	enabled bool
}

// mqttDebugHandler is a slog.Handler that:
//   - always delegates to the wrapped text handler for local syslog output
//   - when enabled, additionally publishes every log record to device/{id}/debug/log via MQTT
type mqttDebugHandler struct {
	text     slog.Handler
	mqtt     mqttclient.MQTTClient
	deviceID string
	state    *debugState
}

func newMQTTDebugHandler(text slog.Handler, mqtt mqttclient.MQTTClient, deviceID string) *mqttDebugHandler {
	return &mqttDebugHandler{
		text:     text,
		mqtt:     mqtt,
		deviceID: deviceID,
		state:    &debugState{},
	}
}

// SetEnabled toggles MQTT log publishing at runtime without restarting the agent.
func (h *mqttDebugHandler) SetEnabled(enabled bool) {
	h.state.mu.Lock()
	h.state.enabled = enabled
	h.state.mu.Unlock()
	slog.Info("debug mode changed", "enabled", enabled)
}

func (h *mqttDebugHandler) mqttEnabled() bool {
	h.state.mu.RLock()
	defer h.state.mu.RUnlock()
	return h.state.enabled
}

// Enabled returns true if the text handler would log the level, OR if MQTT debug is active
// (so that Debug records reach Handle even when the local text level is Info).
func (h *mqttDebugHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.text.Enabled(ctx, level) || h.mqttEnabled()
}

// Handle writes the record locally (if the text handler accepts the level) and, when MQTT
// debug is enabled, publishes it to device/{id}/debug/log (QoS 0, no retain, fire-and-forget).
func (h *mqttDebugHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.text.Enabled(ctx, r.Level) {
		_ = h.text.Handle(ctx, r)
	}

	if !h.mqttEnabled() {
		return nil
	}

	payload := h.marshalRecord(r)
	topic := mqttclient.TopicDebugLog(h.deviceID)
	go func() {
		ctx2, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = h.mqtt.Publish(ctx2, topic, payload, 0, false)
	}()

	return nil
}

func (h *mqttDebugHandler) marshalRecord(r slog.Record) []byte {
	m := map[string]any{
		"ts":    r.Time.UTC().Format(time.RFC3339Nano),
		"level": r.Level.String(),
		"msg":   r.Message,
	}
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = a.Value.Any()
		return true
	})
	b, _ := json.Marshal(m)
	return b
}

// WithAttrs returns a clone sharing the same toggle state but with additional attributes
// pre-applied to the wrapped text handler.
func (h *mqttDebugHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &mqttDebugHandler{
		text:     h.text.WithAttrs(attrs),
		mqtt:     h.mqtt,
		deviceID: h.deviceID,
		state:    h.state,
	}
}

// WithGroup returns a clone with the given group applied to the wrapped text handler.
func (h *mqttDebugHandler) WithGroup(name string) slog.Handler {
	return &mqttDebugHandler{
		text:     h.text.WithGroup(name),
		mqtt:     h.mqtt,
		deviceID: h.deviceID,
		state:    h.state,
	}
}
