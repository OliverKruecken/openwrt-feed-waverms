// Package agent implements the main agent event loop.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/OliverKruecken/waverms-agent/internal/bootstrap"
	"github.com/OliverKruecken/waverms-agent/internal/config"
	mqttclient "github.com/OliverKruecken/waverms-agent/internal/mqtt"
	"github.com/OliverKruecken/waverms-agent/internal/uci"
)

// Command is the payload received on device/{id}/cmd.
type Command struct {
	CmdID   string          `json:"cmd_id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// UCICommandPayload is the inner payload for type "uci_set".
type UCICommandPayload struct {
	Commands []string `json:"commands"`
}

// StateRequestPayload is received on device/{id}/state/request.
type StateRequestPayload struct {
	Packages []string `json:"packages"`
}

// StatePayload is published to device/{id}/state.
type StatePayload struct {
	Timestamp string                                          `json:"timestamp"`
	Trigger   string                                          `json:"trigger"`
	Packages  map[string]map[string][]map[string]interface{} `json:"packages"`
}

// AckPayload is published to device/{id}/ack after command execution.
type AckPayload struct {
	CmdID     string `json:"cmd_id"`
	Status    string `json:"status"`
	Output    string `json:"output"`
	Timestamp string `json:"timestamp"`
}

// InfoPayload is published to device/{id}/info every HeartbeatInterval seconds.
type InfoPayload struct {
	DeviceID      string `json:"device_id"`
	Hostname      string `json:"hostname"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	AgentVersion  string `json:"agent_version"`
	Timestamp     string `json:"timestamp"`
}

// defaultStatePackages is the set of UCI packages exported when the server
// requests state with an empty packages list, or on connect.
var defaultStatePackages = []string{"system", "network", "wireless", "firewall", "dhcp"}

// Options holds all dependencies for the Agent.
type Options struct {
	Config  *config.Config
	Creds   *config.Credentials
	MAC     string
	MQTT    mqttclient.MQTTClient
	UCI     uci.UCIRunner
	Version string
	// BootstrapOpts overrides the default bootstrap options (useful in tests).
	BootstrapOpts *bootstrap.Options
	// BootstrapTokenPath is used if BootstrapOpts is nil. Defaults to /etc/waverms/bootstrap_token.
	BootstrapTokenPath string
	// CredsPath is where credentials are stored. Defaults to /etc/waverms/credentials.
	CredsPath string
}

// Agent is the main runtime component.
type Agent struct {
	cfg     *config.Config
	creds   *config.Credentials
	mac     string
	mqtt    mqttclient.MQTTClient
	uci     uci.UCIRunner
	version string

	bootstrapTokenPath string
	credsPath          string
}

// New creates an Agent from the provided options.
func New(opts *Options) *Agent {
	tokenPath := opts.BootstrapTokenPath
	if tokenPath == "" {
		tokenPath = "/etc/waverms/bootstrap_token"
	}
	credsPath := opts.CredsPath
	if credsPath == "" {
		credsPath = "/etc/waverms/credentials"
	}

	return &Agent{
		cfg:                opts.Config,
		creds:              opts.Creds,
		mac:                opts.MAC,
		mqtt:               opts.MQTT,
		uci:                opts.UCI,
		version:            opts.Version,
		bootstrapTokenPath: tokenPath,
		credsPath:          credsPath,
	}
}

// Run is the main entry point. It bootstraps if needed, then enters the
// reconnect loop. It returns nil when ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	if !a.creds.HasCredentials() {
		bopts := bootstrap.Options{
			TokenPath:    a.bootstrapTokenPath,
			CredsPath:    a.credsPath,
			MAC:          a.mac,
			AgentVersion: a.version,
		}
		resp, err := bootstrap.Run(ctx, a.cfg, bopts, a.mqtt)
		if err != nil {
			return fmt.Errorf("bootstrap failed: %w", err)
		}
		// Reload credentials from the just-written file.
		creds, err := config.LoadCredentials(a.credsPath)
		if err != nil {
			return fmt.Errorf("load credentials after bootstrap: %w", err)
		}
		a.creds = creds
		// Update broker host/port if the server provided overrides.
		if resp.BrokerHost != "" {
			a.cfg.BrokerHost = resp.BrokerHost
		}
		if resp.BrokerPort > 0 {
			a.cfg.BrokerPort = resp.BrokerPort
		}
	}

	delay := time.Second
	maxDelay := 300 * time.Second

	for {
		err := a.runSession(ctx)
		if ctx.Err() != nil {
			return nil
		}
		log.Printf("session ended (%v), reconnecting in %v", err, delay)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
		delay = min(delay*2, maxDelay)
	}
}

// runSession connects the device credentials, subscribes to the command topic,
// sends an initial heartbeat, and then drives the heartbeat loop until the
// connection is lost or ctx is cancelled.
func (a *Agent) runSession(ctx context.Context) error {
	lwt := &mqttclient.LWT{
		Topic:   mqttclient.TopicStatus(a.creds.DeviceID),
		Payload: []byte("offline"),
		QoS:     1,
		Retain:  true,
	}

	connOpts := mqttclient.ConnectOptions{
		BrokerHost: a.cfg.BrokerHost,
		BrokerPort: a.cfg.BrokerPort,
		ClientID:   a.creds.DeviceID,
		Username:   a.creds.DeviceID,
		Password:   a.creds.Secret,
		LWT:        lwt,
	}

	if err := a.mqtt.Connect(ctx, connOpts); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	if err := a.mqtt.Subscribe(ctx, mqttclient.TopicCommand(a.creds.DeviceID), 1, a.handleCommand); err != nil {
		return fmt.Errorf("subscribe cmd: %w", err)
	}

	if err := a.mqtt.Subscribe(ctx, mqttclient.TopicStateRequest(a.creds.DeviceID), 1, a.handleStateRequest); err != nil {
		return fmt.Errorf("subscribe state/request: %w", err)
	}

	if err := a.publishInfo(ctx); err != nil {
		log.Printf("initial heartbeat failed: %v", err)
	}

	if err := a.publishState(ctx, "connect", nil); err != nil {
		log.Printf("initial state publish failed: %v", err)
	}

	heartbeatInterval := time.Duration(a.cfg.HeartbeatInterval) * time.Second
	if heartbeatInterval <= 0 {
		heartbeatInterval = 60 * time.Second
	}
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	disconnCh := a.mqtt.Disconnected()

	for {
		select {
		case <-ctx.Done():
			a.mqtt.Disconnect()
			return nil
		case <-disconnCh:
			return fmt.Errorf("connection lost")
		case <-ticker.C:
			if err := a.publishInfo(ctx); err != nil {
				log.Printf("heartbeat failed: %v", err)
			}
		}
	}
}

// publishInfo sends the current device status to device/{id}/info.
func (a *Agent) publishInfo(ctx context.Context) error {
	hostname := readHostname()
	uptime := readUptimeSeconds()

	info := InfoPayload{
		DeviceID:      a.creds.DeviceID,
		Hostname:      hostname,
		UptimeSeconds: uptime,
		AgentVersion:  a.version,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
	}

	payload, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshal info: %w", err)
	}

	return a.mqtt.Publish(ctx, mqttclient.TopicInfo(a.creds.DeviceID), payload, 1, false)
}

// handleCommand processes an incoming ad-hoc command.
func (a *Agent) handleCommand(topic string, payload []byte) {
	var cmd Command
	if err := json.Unmarshal(payload, &cmd); err != nil {
		log.Printf("invalid command payload on %s: %v", topic, err)
		return
	}

	var output strings.Builder
	status := "ok"

	if cmd.Type == "uci_set" {
		var uciPayload UCICommandPayload
		if err := json.Unmarshal(cmd.Payload, &uciPayload); err != nil {
			log.Printf("invalid uci_set payload for cmd %s: %v", cmd.CmdID, err)
			a.publishAck(cmd.CmdID, "error", fmt.Sprintf("invalid payload: %v", err))
			return
		}
		for _, rawCmd := range uciPayload.Commands {
			// Strip leading "uci " prefix if present.
			args := strings.Fields(strings.TrimPrefix(rawCmd, "uci "))
			if len(args) == 0 {
				continue
			}
			out, err := a.uci.ExecRaw(args...)
			if out != "" {
				output.WriteString(out)
				output.WriteString("\n")
			}
			if err != nil {
				output.WriteString(fmt.Sprintf("error: %v\n", err))
				status = "error"
			}
		}
	} else {
		output.WriteString(fmt.Sprintf("unknown command type: %s", cmd.Type))
		status = "error"
	}

	a.publishAck(cmd.CmdID, status, strings.TrimSpace(output.String()))
}

// publishAck sends the command acknowledgement to device/{id}/ack.
func (a *Agent) publishAck(cmdID, status, output string) {
	ack := AckPayload{
		CmdID:     cmdID,
		Status:    status,
		Output:    output,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	payload, err := json.Marshal(ack)
	if err != nil {
		log.Printf("marshal ack: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.mqtt.Publish(ctx, mqttclient.TopicAck(a.creds.DeviceID), payload, 1, false); err != nil {
		log.Printf("publish ack for %s: %v", cmdID, err)
	}
}

// handleStateRequest processes an incoming state/request message.
func (a *Agent) handleStateRequest(topic string, payload []byte) {
	var req StateRequestPayload
	if err := json.Unmarshal(payload, &req); err != nil {
		log.Printf("invalid state/request payload on %s: %v", topic, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.publishState(ctx, "request", req.Packages); err != nil {
		log.Printf("publish state on request: %v", err)
	}
}

// publishState collects UCI state for the given packages and publishes it
// to device/{id}/state. If pkgs is empty, defaultStatePackages is used.
// Packages that fail to export are skipped and logged.
func (a *Agent) publishState(ctx context.Context, trigger string, pkgs []string) error {
	if len(pkgs) == 0 {
		pkgs = defaultStatePackages
	}

	packages := make(map[string]map[string][]map[string]interface{})
	for _, pkg := range pkgs {
		out, err := a.uci.Export(pkg)
		if err != nil {
			log.Printf("uci export %s: %v (skipping)", pkg, err)
			continue
		}
		sections, err := uci.ParseUCIExport(out)
		if err != nil {
			log.Printf("parse uci export %s: %v (skipping)", pkg, err)
			continue
		}
		if len(sections) > 0 {
			packages[pkg] = sections
		}
	}

	state := StatePayload{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Trigger:   trigger,
		Packages:  packages,
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	return a.mqtt.Publish(ctx, mqttclient.TopicState(a.creds.DeviceID), data, 1, false)
}

// readHostname reads the kernel hostname from /proc/sys/kernel/hostname.
// Returns "unknown" on any error.
func readHostname() string {
	data, err := os.ReadFile("/proc/sys/kernel/hostname")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(data))
}

// readUptimeSeconds reads the system uptime from /proc/uptime.
// Returns 0 on any error.
func readUptimeSeconds() int64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	parts := strings.Fields(string(data))
	if len(parts) == 0 {
		return 0
	}
	var secs float64
	fmt.Sscanf(parts[0], "%f", &secs)
	return int64(secs)
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
