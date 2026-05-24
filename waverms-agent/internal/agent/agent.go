// Package agent implements the main agent event loop.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OliverKruecken/waverms-agent/internal/apply"
	"github.com/OliverKruecken/waverms-agent/internal/bootstrap"
	"github.com/OliverKruecken/waverms-agent/internal/config"
	"github.com/OliverKruecken/waverms-agent/internal/filewriter"
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

// FileWriteCommandPayload is the inner payload for type "file_write".
type FileWriteCommandPayload struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    string `json:"mode"`
}

// HostKeyRestorePayload is the inner payload for type "host_key_restore".
// Keys maps each filename (e.g. "ssh_host_ed25519_key") to its file content.
// All files are written under /etc/dropbear/ with mode 0600.
type HostKeyRestorePayload struct {
	Keys map[string]string `json:"keys"`
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
	DeviceID       string   `json:"device_id"`
	Hostname       string   `json:"hostname"`
	UptimeSeconds  int64    `json:"uptime_seconds"`
	AgentVersion   string   `json:"agent_version"`
	Model          string   `json:"model"`
	OpenWrtVersion string   `json:"openwrt_version"`
	Timestamp      string   `json:"timestamp"`
	Capabilities   []string `json:"capabilities"`
}

// supportedCapabilities is the list of command types this agent version supports.
// Append to this list whenever a new command type is implemented.
var supportedCapabilities = []string{
	"uci_set",
	"config_apply",
	"file_write",
	"state_report",
	"host_key_restore",
}

// fallbackStatePackages is used only when /etc/config cannot be read.
var fallbackStatePackages = []string{"system", "network", "wireless", "firewall", "dhcp"}

// discoverPackages lists all regular files in /etc/config and returns them as
// UCI package names. Falls back to fallbackStatePackages on any read error.
func discoverPackages() []string {
	entries, err := os.ReadDir("/etc/config")
	if err != nil {
		return fallbackStatePackages
	}
	pkgs := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Type().IsRegular() {
			pkgs = append(pkgs, e.Name())
		}
	}
	if len(pkgs) == 0 {
		return fallbackStatePackages
	}
	return pkgs
}

// Options holds all dependencies for the Agent.
type Options struct {
	Config         *config.Config
	Creds          *config.Credentials
	MAC            string
	Model          string
	OpenWrtVersion string
	MQTT           mqttclient.MQTTClient
	UCI            uci.UCIRunner
	Version        string
	// FileWriter is used to write files on the device filesystem. Defaults to OSFileWriter.
	FileWriter filewriter.FileWriter
	// BootstrapOpts overrides the default bootstrap options (useful in tests).
	BootstrapOpts *bootstrap.Options
	// BootstrapTokenPath is used if BootstrapOpts is nil. Defaults to /etc/waverms/bootstrap_token.
	BootstrapTokenPath string
	// CredsPath is where credentials are stored. Defaults to /etc/waverms/credentials.
	CredsPath string
}

// Agent is the main runtime component.
type Agent struct {
	cfg            *config.Config
	creds          *config.Credentials
	mac            string
	model          string
	openwrtVersion string
	mqtt           mqttclient.MQTTClient
	uci            uci.UCIRunner
	fileWriter     filewriter.FileWriter
	version        string

	bootstrapTokenPath string
	credsPath          string
	debugHandler       *mqttDebugHandler
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

	fw := opts.FileWriter
	if fw == nil {
		fw = &filewriter.OSFileWriter{}
	}

	return &Agent{
		cfg:                opts.Config,
		creds:              opts.Creds,
		mac:                opts.MAC,
		model:              opts.Model,
		openwrtVersion:     opts.OpenWrtVersion,
		mqtt:               opts.MQTT,
		uci:                opts.UCI,
		fileWriter:         fw,
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
			TokenPath:      a.bootstrapTokenPath,
			CredsPath:      a.credsPath,
			MAC:            a.mac,
			Model:          a.model,
			OpenWrtVersion: a.openwrtVersion,
			AgentVersion:   a.version,
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

	// Replace the global slog default with the MQTT-aware debug handler now that we
	// have credentials. A fresh TextHandler is used as the inner handler to avoid a
	// circular dependency: slog.Default().Handler() (the stdlib defaultHandler) routes
	// writes back through log, but slog.SetDefault redirects log → our handler → cycle.
	localLevel := slog.LevelInfo
	if a.cfg.Debug {
		localLevel = slog.LevelDebug
	}
	textHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: localLevel})
	a.debugHandler = newMQTTDebugHandler(textHandler, a.mqtt, a.creds.DeviceID)
	slog.SetDefault(slog.New(a.debugHandler))
	log.SetFlags(0) // TextHandler owns timestamps; avoid double prefix on log.Printf calls

	delay := time.Second
	maxDelay := 300 * time.Second

	for {
		err := a.runSession(ctx)
		if ctx.Err() != nil {
			return nil
		}
		slog.Info("session ended, reconnecting", "err", err, "delay", delay)
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
	slog.Debug("connecting", "host", a.cfg.BrokerHost, "port", a.cfg.BrokerPort, "client_id", a.creds.DeviceID)

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
	slog.Debug("connected", "client_id", a.creds.DeviceID)

	cmdTopic := mqttclient.TopicCommand(a.creds.DeviceID)
	if err := a.mqtt.Subscribe(ctx, cmdTopic, 1, a.handleCommand); err != nil {
		return fmt.Errorf("subscribe cmd: %w", err)
	}
	slog.Debug("subscribed", "topic", cmdTopic)

	stateReqTopic := mqttclient.TopicStateRequest(a.creds.DeviceID)
	if err := a.mqtt.Subscribe(ctx, stateReqTopic, 1, a.handleStateRequest); err != nil {
		return fmt.Errorf("subscribe state/request: %w", err)
	}
	slog.Debug("subscribed", "topic", stateReqTopic)

	controlTopic := mqttclient.TopicDebugControl(a.creds.DeviceID)
	if err := a.mqtt.Subscribe(ctx, controlTopic, 1, a.handleDebugControl); err != nil {
		return fmt.Errorf("subscribe debug/control: %w", err)
	}
	slog.Debug("subscribed", "topic", controlTopic)

	if err := a.publishInfo(ctx); err != nil {
		slog.Error("initial heartbeat failed", "err", err)
	}

	if err := a.publishState(ctx, "connect", nil); err != nil {
		slog.Error("initial state publish failed", "err", err)
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
				slog.Error("heartbeat failed", "err", err)
			}
		}
	}
}

// publishInfo sends the current device status to device/{id}/info.
func (a *Agent) publishInfo(ctx context.Context) error {
	hostname := readHostname()
	uptime := readUptimeSeconds()
	slog.Debug("publishing heartbeat", "hostname", hostname, "uptime_seconds", uptime)

	info := InfoPayload{
		DeviceID:       a.creds.DeviceID,
		Hostname:       hostname,
		UptimeSeconds:  uptime,
		AgentVersion:   a.version,
		Model:          a.model,
		OpenWrtVersion: a.openwrtVersion,
		Timestamp:      time.Now().UTC().Format(time.RFC3339),
		Capabilities:   supportedCapabilities,
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
		slog.Error("invalid command payload", "topic", topic, "err", err)
		return
	}
	slog.Debug("received command", "cmd_id", cmd.CmdID, "type", cmd.Type)

	var output strings.Builder
	status := "ok"

	switch cmd.Type {
	case "uci_set":
		var uciPayload UCICommandPayload
		if err := json.Unmarshal(cmd.Payload, &uciPayload); err != nil {
			slog.Error("invalid uci_set payload", "cmd_id", cmd.CmdID, "err", err)
			a.publishAck(cmd.CmdID, "error", fmt.Sprintf("invalid payload: %v", err))
			return
		}
		for _, rawCmd := range uciPayload.Commands {
			// Strip leading "uci " prefix if present.
			args := strings.Fields(strings.TrimPrefix(rawCmd, "uci "))
			if len(args) == 0 {
				continue
			}
			slog.Debug("uci exec", "args", args)
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

	case "config_apply":
		applier := apply.New(a.uci)
		committed, err := applier.Apply(cmd.Payload)
		if err != nil {
			slog.Error("config_apply failed", "cmd_id", cmd.CmdID, "err", err)
			a.publishAck(cmd.CmdID, "error", err.Error())
			return
		}
		slog.Info("config_apply succeeded", "cmd_id", cmd.CmdID, "packages", committed)
		if reloadErrs := apply.RunReloads(committed); len(reloadErrs) > 0 {
			slog.Warn("config_apply: service reload failures", "errors", strings.Join(reloadErrs, "; "))
		}
		a.publishAck(cmd.CmdID, "ok", "")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := a.publishState(ctx, "apply_success", committed); err != nil {
			slog.Error("publish state after config_apply", "err", err)
		}
		return

	case "file_write":
		var fwPayload FileWriteCommandPayload
		if err := json.Unmarshal(cmd.Payload, &fwPayload); err != nil {
			slog.Error("invalid file_write payload", "cmd_id", cmd.CmdID, "err", err)
			a.publishAck(cmd.CmdID, "error", fmt.Sprintf("invalid payload: %v", err))
			return
		}
		if fwPayload.Path == "" {
			a.publishAck(cmd.CmdID, "error", "path is required")
			return
		}
		// Parse mode octal string ("0600" → os.FileMode). Default to 0600 if missing or invalid.
		perm := os.FileMode(0600)
		if fwPayload.Mode != "" {
			var modeInt uint32
			if _, err := fmt.Sscanf(fwPayload.Mode, "%o", &modeInt); err == nil {
				perm = os.FileMode(modeInt)
			}
		}
		if err := a.fileWriter.WriteFile(fwPayload.Path, []byte(fwPayload.Content), perm); err != nil {
			slog.Error("file_write failed", "cmd_id", cmd.CmdID, "path", fwPayload.Path, "err", err)
			a.publishAck(cmd.CmdID, "error", fmt.Sprintf("write %s: %v", fwPayload.Path, err))
			return
		}
		slog.Info("file_write succeeded", "cmd_id", cmd.CmdID, "path", fwPayload.Path)
		a.publishAck(cmd.CmdID, "ok", "")
		return

	case "host_key_restore":
		var hkPayload HostKeyRestorePayload
		if err := json.Unmarshal(cmd.Payload, &hkPayload); err != nil {
			slog.Error("invalid host_key_restore payload", "cmd_id", cmd.CmdID, "err", err)
			a.publishAck(cmd.CmdID, "error", fmt.Sprintf("invalid payload: %v", err))
			return
		}
		if len(hkPayload.Keys) == 0 {
			a.publishAck(cmd.CmdID, "error", "keys map is empty")
			return
		}
		for name, content := range hkPayload.Keys {
			// filepath.Base guards against path-traversal in the filename key.
			safe := filepath.Base(name)
			path := "/etc/dropbear/" + safe
			if err := a.fileWriter.WriteFile(path, []byte(content), 0600); err != nil {
				slog.Error("host_key_restore: write failed", "cmd_id", cmd.CmdID, "file", safe, "err", err)
				a.publishAck(cmd.CmdID, "error", fmt.Sprintf("write %s: %v", safe, err))
				return
			}
			slog.Debug("host_key_restore: wrote key file", "file", safe)
		}
		// Restart Dropbear so it picks up the restored keys.
		if _, err := a.uci.ExecRaw("/etc/init.d/dropbear", "restart"); err != nil {
			// Non-fatal: keys are already on disk; dropbear will use them on next connection.
			slog.Warn("host_key_restore: dropbear restart failed (non-fatal)", "cmd_id", cmd.CmdID, "err", err)
		}
		slog.Info("host_key_restore succeeded", "cmd_id", cmd.CmdID, "files", len(hkPayload.Keys))
		a.publishAck(cmd.CmdID, "ok", "")
		return

	default:
		output.WriteString(fmt.Sprintf("unknown command type: %s", cmd.Type))
		status = "error"
	}

	a.publishAck(cmd.CmdID, status, strings.TrimSpace(output.String()))
}

// publishAck sends the command acknowledgement to device/{id}/ack.
func (a *Agent) publishAck(cmdID, status, output string) {
	slog.Debug("publishing ack", "cmd_id", cmdID, "status", status)
	ack := AckPayload{
		CmdID:     cmdID,
		Status:    status,
		Output:    output,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	payload, err := json.Marshal(ack)
	if err != nil {
		slog.Error("marshal ack", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.mqtt.Publish(ctx, mqttclient.TopicAck(a.creds.DeviceID), payload, 1, false); err != nil {
		slog.Error("publish ack", "cmd_id", cmdID, "err", err)
	}
}

// handleStateRequest processes an incoming state/request message.
func (a *Agent) handleStateRequest(topic string, payload []byte) {
	var req StateRequestPayload
	if err := json.Unmarshal(payload, &req); err != nil {
		slog.Error("invalid state/request payload", "topic", topic, "err", err)
		return
	}
	slog.Debug("state request received", "packages", req.Packages)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.publishState(ctx, "request", req.Packages); err != nil {
		slog.Error("publish state on request", "err", err)
	}
}

// handleDebugControl toggles MQTT debug log publishing at runtime.
// The server publishes {"enabled": true|false} to device/{id}/debug/control (retain: true).
func (a *Agent) handleDebugControl(_ string, payload []byte) {
	var msg struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		slog.Error("invalid debug/control payload", "err", err)
		return
	}
	if a.debugHandler != nil {
		a.debugHandler.SetEnabled(msg.Enabled)
	}
}

// publishState collects UCI state for the given packages and publishes it
// to device/{id}/state. If pkgs is empty, defaultStatePackages is used.
// Packages that fail to export are skipped and logged.
func (a *Agent) publishState(ctx context.Context, trigger string, pkgs []string) error {
	if len(pkgs) == 0 {
		pkgs = discoverPackages()
	}
	slog.Debug("publishing state", "trigger", trigger, "packages", pkgs)

	packages := make(map[string]map[string][]map[string]interface{})
	for _, pkg := range pkgs {
		out, err := a.uci.Export(pkg)
		if err != nil {
			slog.Warn("uci export failed, skipping", "package", pkg, "err", err)
			continue
		}
		sections, err := uci.ParseUCIExport(out)
		if err != nil {
			slog.Warn("uci export parse failed, skipping", "package", pkg, "err", err)
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
