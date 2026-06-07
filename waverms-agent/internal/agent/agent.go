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
	"sync"
	"time"

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
//
// Format controls how Content is decoded before writing:
//   - "openssh" or "" (default): Content is a UTF-8 string, written as-is.
//   - "dropbear": Content is standard base64 (RFC 4648); the agent decodes it to raw bytes.
//     Required for native Dropbear binary host key files that are not valid UTF-8.
type FileWriteCommandPayload struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Format  string `json:"format"` // "openssh" (default/absent) | "dropbear" (base64 binary)
	Mode    string `json:"mode"`
}

// HostKeyRestorePayload is the inner payload for type "host_key_restore".
// Keys maps each filename (e.g. "dropbear_ed25519_host_key") to its raw file content.
// Values are base64-encoded in the JSON payload (RFC 4648); encoding/json decodes them
// automatically into []byte so they can be written as binary to disk.
// Files are written to the SSH daemon's key directory (detected at startup) with mode 0600.
type HostKeyRestorePayload struct {
	Keys map[string][]byte `json:"keys"`
}

// sshDaemonInfo describes the SSH daemon running on this device.
type sshDaemonInfo struct {
	Name    string // "dropbear" or "openssh"
	Dir     string // directory where host keys live, e.g. "/etc/dropbear/"
	Service string // init script path, e.g. "/etc/init.d/dropbear"
}

var (
	daemonDropbear = sshDaemonInfo{Name: "dropbear", Dir: "/etc/dropbear/", Service: "/etc/init.d/dropbear"}
	daemonOpenSSH  = sshDaemonInfo{Name: "openssh", Dir: "/etc/ssh/", Service: "/etc/init.d/sshd"}
)

// detectSSHDaemon checks which SSH daemon binary is present and returns its
// sshDaemonInfo. Falls back to Dropbear when neither binary is found.
func detectSSHDaemon() sshDaemonInfo {
	if _, err := os.Stat("/usr/sbin/sshd"); err == nil {
		return daemonOpenSSH
	}
	return daemonDropbear
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
	// RawFiles contains the verbatim content of /etc/config/<pkg> for each package,
	// preserving comments and formatting that uci export strips.
	RawFiles map[string]string `json:"raw_files,omitempty"`
}

// AckPayload is published to device/{id}/ack after command execution.
// Keys is non-nil only for host_key_fetch acks; encoding/json base64-encodes []byte automatically.
type AckPayload struct {
	CmdID     string            `json:"cmd_id"`
	Status    string            `json:"status"`
	Output    string            `json:"output"`
	Timestamp string            `json:"timestamp"`
	Keys      map[string][]byte `json:"keys,omitempty"`
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

// allowedUCISubcmds is the set of UCI subcommands that uci_set is permitted to
// execute. Only these safe, non-destructive subcommands are accepted; anything
// else (e.g. "batch", "import") is rejected to prevent command injection if the
// MQTT broker is compromised or a message is replayed.
var allowedUCISubcmds = map[string]bool{
	"set":      true,
	"commit":   true,
	"revert":   true,
	"delete":   true,
	"add":      true,
	"add_list": true,
	"del_list": true,
	"get":      true,
}

// allowedHostKeyFilenamesByDaemon maps each SSH daemon to the set of host-key
// filenames it is permitted to write/remove. filepath.Base() strips directory
// components before the check to prevent path-traversal attacks.
//
// Dropbear accepts both its own binary format and OpenSSH PEM format.
// OpenSSH only uses its own PEM format — accepting Dropbear filenames would
// write files that OpenSSH ignores, which is misleading and unnecessary.
var allowedHostKeyFilenamesByDaemon = map[string]map[string]bool{
	"dropbear": {
		"dropbear_ed25519_host_key":     true,
		"dropbear_rsa_host_key":         true,
		"dropbear_ed25519_host_key.pub": true,
		"dropbear_rsa_host_key.pub":     true,
		"ssh_host_ed25519_key":          true,
		"ssh_host_rsa_key":              true,
		"ssh_host_ed25519_key.pub":      true,
		"ssh_host_rsa_key.pub":          true,
	},
	"openssh": {
		"ssh_host_ed25519_key":     true,
		"ssh_host_rsa_key":         true,
		"ssh_host_ed25519_key.pub": true,
		"ssh_host_rsa_key.pub":     true,
	},
}

// fileWriteAllowedDirs is the directory allowlist for the file_write command.
// Only paths whose filepath.Clean result begins with one of these prefixes are
// accepted. This prevents a compromised or replayed MQTT message from overwriting
// arbitrary system files (e.g. /etc/shadow, /usr/bin/waverms-agent).
var fileWriteAllowedDirs = []string{
	"/etc/dropbear/",
	"/etc/ssh/",
	"/etc/waverms/",
	"/tmp/",
}

// isFileWritePathAllowed returns true when path, after filepath.Clean, begins
// with one of the entries in fileWriteAllowedDirs.
func isFileWritePathAllowed(path string) bool {
	clean := filepath.Clean(path)
	for _, prefix := range fileWriteAllowedDirs {
		if strings.HasPrefix(clean, prefix) {
			return true
		}
	}
	return false
}

// supportedCapabilities is the list of command types this agent version supports.
// Append to this list whenever a new command type is implemented.
var supportedCapabilities = []string{
	"uci_set",
	"config_apply",
	"config_confirm",
	"file_write",
	"state_report",
	"host_key_restore",
	"host_key_remove",
	"host_key_fetch",
	"set_password",
	"tls_cert_push",
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
		} else if e.Type()&os.ModeSymlink != 0 {
			// Some OpenWrt setups symlink UCI packages into /etc/config/.
			// We skip symlinks because os.ReadDir does not follow them and
			// resolving them safely would require extra stat calls. Log at
			// debug so operators can spot unexpected symlinks.
			slog.Debug("discoverPackages: skipping symlink in /etc/config", "name", e.Name())
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
	// FileAccess is used to read/write files on the device filesystem. Defaults to OSFileAccess.
	FileAccess filewriter.FileAccess
	// PasswordSetter is used to set system user passwords. Defaults to OSPasswordSetter.
	PasswordSetter PasswordSetter
	// BootstrapOpts overrides the default bootstrap options (useful in tests).
	BootstrapOpts *bootstrap.Options
	// BootstrapTokenPath is used if BootstrapOpts is nil. Defaults to /etc/waverms/bootstrap_token.
	BootstrapTokenPath string
	// CredsPath is where credentials are stored. Defaults to /etc/waverms/credentials.
	CredsPath string
	// SSHDaemon overrides the SSH daemon detection (useful in tests). Defaults to detectSSHDaemon().
	SSHDaemon *sshDaemonInfo
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
	fileAccess     filewriter.FileAccess
	passwordSetter PasswordSetter
	version        string
	sshDaemon      sshDaemonInfo

	bootstrapTokenPath string
	credsPath          string
	debugHandler       *mqttDebugHandler
	cmdHandlers        map[string]func(Command)

	// sessionMu protects sessionDisconnCh, which is set for the lifetime of
	// each MQTT session and read by the config_apply connectivity watchdog.
	sessionMu        sync.RWMutex
	sessionDisconnCh <-chan struct{}

	// pendingAckMu protects pendingAck, which holds a deferred ACK that must
	// be sent on the next session start after a config_apply rollback.
	pendingAckMu sync.Mutex
	pendingAck   *rollbackAck

	// confirmMu protects the active watchdog confirm channel.
	// When a config_apply watchdog is running, confirmCmdID and confirmCh are
	// set. handleConfigConfirm closes confirmCh to cut the watchdog short.
	confirmMu    sync.Mutex
	confirmCmdID string
	confirmCh    chan struct{}
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

	fw := opts.FileAccess
	if fw == nil {
		fw = &filewriter.OSFileAccess{}
	}
	ps := opts.PasswordSetter
	if ps == nil {
		ps = &OSPasswordSetter{}
	}

	daemon := daemonDropbear
	if opts.SSHDaemon != nil {
		daemon = *opts.SSHDaemon
	} else {
		daemon = detectSSHDaemon()
	}

	a := &Agent{
		cfg:                opts.Config,
		creds:              opts.Creds,
		mac:                opts.MAC,
		model:              opts.Model,
		openwrtVersion:     opts.OpenWrtVersion,
		mqtt:               opts.MQTT,
		uci:                opts.UCI,
		fileAccess:         fw,
		passwordSetter:     ps,
		version:            opts.Version,
		sshDaemon:          daemon,
		bootstrapTokenPath: tokenPath,
		credsPath:          credsPath,
	}
	a.cmdHandlers = map[string]func(Command){
		"uci_set":          a.handleUCISet,
		"config_apply":     a.handleConfigApply,
		"config_confirm":   a.handleConfigConfirm,
		"file_write":       a.handleFileWrite,
		"host_key_restore": a.handleHostKeyRestore,
		"host_key_remove":  a.handleHostKeyRemove,
		"host_key_fetch":   a.handleHostKeyFetch,
		"set_password":     a.handleSetPassword,
		"tls_cert_push":    a.handleTlsCertPush,
	}
	return a
}

func (a *Agent) setWatchdogConfirm(cmdID string, ch chan struct{}) {
	a.confirmMu.Lock()
	a.confirmCmdID = cmdID
	a.confirmCh = ch
	a.confirmMu.Unlock()
}

func (a *Agent) clearWatchdogConfirm(cmdID string) {
	a.confirmMu.Lock()
	if a.confirmCmdID == cmdID {
		a.confirmCmdID = ""
		a.confirmCh = nil
	}
	a.confirmMu.Unlock()
}

// signalWatchdogConfirm closes the stored confirm channel if cmdID matches the
// active watchdog. Returns true when the signal was delivered.
func (a *Agent) signalWatchdogConfirm(cmdID string) bool {
	a.confirmMu.Lock()
	defer a.confirmMu.Unlock()
	if a.confirmCmdID == cmdID {
		close(a.confirmCh)
		a.confirmCh = nil
		a.confirmCmdID = ""
		return true
	}
	return false
}

// handleConfigConfirm is dispatched when the server sends a config_confirm
// command referencing an in-flight config_apply. It signals the connectivity
// watchdog to confirm the apply immediately instead of waiting for the timeout.
func (a *Agent) handleConfigConfirm(cmd Command) {
	if !a.signalWatchdogConfirm(cmd.CmdID) {
		slog.Warn("config_confirm: no active watchdog", "cmd_id", cmd.CmdID)
	}
}

func (a *Agent) setSessionDisconnCh(ch <-chan struct{}) {
	a.sessionMu.Lock()
	a.sessionDisconnCh = ch
	a.sessionMu.Unlock()
}

func (a *Agent) getSessionDisconnCh() <-chan struct{} {
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	return a.sessionDisconnCh
}

func (a *Agent) setPendingRollbackAck(ack *rollbackAck) {
	a.pendingAckMu.Lock()
	a.pendingAck = ack
	a.pendingAckMu.Unlock()
}

// takePendingRollbackAck atomically reads and clears the pending rollback ACK.
func (a *Agent) takePendingRollbackAck() *rollbackAck {
	a.pendingAckMu.Lock()
	defer a.pendingAckMu.Unlock()
	ack := a.pendingAck
	a.pendingAck = nil
	return ack
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
		// Retry bootstrap with exponential backoff so transient DNS / network
		// failures at startup (e.g. dnsmasq not ready yet) don't crash the
		// process and exhaust procd's respawn limit.
		bDelay := 5 * time.Second
		const bMaxDelay = 5 * time.Minute
		var resp *bootstrap.BootstrapResponse
		for {
			var err error
			resp, err = bootstrap.Run(ctx, a.cfg, bopts, a.mqtt)
			if err == nil {
				break
			}
			if ctx.Err() != nil {
				return nil
			}
			slog.Info("bootstrap failed, retrying", "err", err, "delay", bDelay)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(bDelay):
			}
			bDelay = min(bDelay*2, bMaxDelay)
		}
		// Bootstrap uses the shared MQTT client for its temporary connection.
		// Disconnect it explicitly now so the TCP socket is released before
		// runSession opens a new session with the permanent device credentials.
		a.mqtt.Disconnect()

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
	// A session that stays connected longer than this threshold is considered
	// healthy; on disconnect the backoff resets so the next reconnect is fast.
	const sessionHealthyThreshold = 30 * time.Second

	for {
		sessionStart := time.Now()
		err := a.runSession(ctx)
		if ctx.Err() != nil {
			return nil
		}
		// Reset the backoff delay when the session ran long enough to be
		// considered stable. Without this, a device that reconnects after days
		// of uptime would wait up to 300 s before its first reconnect attempt.
		if time.Since(sessionStart) >= sessionHealthyThreshold {
			delay = time.Second
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

	// subscribeOrDisconnect subscribes and disconnects (releasing the TCP fd)
	// if the subscription fails, so that the next reconnect attempt starts clean.
	subscribeOrDisconnect := func(topic string, qos byte, handler mqttclient.MessageHandler) error {
		if err := a.mqtt.Subscribe(ctx, topic, qos, handler); err != nil {
			a.mqtt.Disconnect()
			return err
		}
		return nil
	}

	cmdTopic := mqttclient.TopicCommand(a.creds.DeviceID)
	if err := subscribeOrDisconnect(cmdTopic, 1, a.handleCommand); err != nil {
		return fmt.Errorf("subscribe cmd: %w", err)
	}
	slog.Debug("subscribed", "topic", cmdTopic)

	stateReqTopic := mqttclient.TopicStateRequest(a.creds.DeviceID)
	if err := subscribeOrDisconnect(stateReqTopic, 1, a.handleStateRequest); err != nil {
		return fmt.Errorf("subscribe state/request: %w", err)
	}
	slog.Debug("subscribed", "topic", stateReqTopic)

	controlTopic := mqttclient.TopicDebugControl(a.creds.DeviceID)
	if err := subscribeOrDisconnect(controlTopic, 1, a.handleDebugControl); err != nil {
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
	a.setSessionDisconnCh(disconnCh)
	defer a.setSessionDisconnCh(nil)

	// Send a deferred rollback ACK if a previous config_apply was rolled back
	// because connectivity was lost before the watchdog confirmed the apply.
	if ack := a.takePendingRollbackAck(); ack != nil {
		a.publishAck(ack.cmdID, ack.status, ack.output)
	}

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
			// Safety net: send a rollback ACK that was set after session start
			// (e.g. when the rollback goroutine races with a fast reconnect).
			if ack := a.takePendingRollbackAck(); ack != nil {
				a.publishAck(ack.cmdID, ack.status, ack.output)
			}
		}
	}
}

// publishInfo sends the current device status to device/{id}/info.
func (a *Agent) publishInfo(ctx context.Context) error {
	hostname := readHostname()
	uptime := readUptimeSeconds()
	slog.Debug("publishing heartbeat", "hostname", hostname, "uptime_seconds", uptime)

	caps := append(supportedCapabilities, "ssh_daemon:"+a.sshDaemon.Name)
	info := InfoPayload{
		DeviceID:       a.creds.DeviceID,
		Hostname:       hostname,
		UptimeSeconds:  uptime,
		AgentVersion:   a.version,
		Model:          a.model,
		OpenWrtVersion: a.openwrtVersion,
		Timestamp:      time.Now().UTC().Format(time.RFC3339),
		Capabilities:   caps,
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

	handler, ok := a.cmdHandlers[cmd.Type]
	if !ok {
		a.publishAck(cmd.CmdID, "error", fmt.Sprintf("unknown command type: %s", cmd.Type))
		return
	}
	handler(cmd)
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

// publishAckKeys publishes an ACK_OK carrying the fetched host key map (host_key_fetch).
func (a *Agent) publishAckKeys(cmdID string, keys map[string][]byte) {
	slog.Debug("publishing ack keys", "cmd_id", cmdID, "files", len(keys))
	ack := AckPayload{
		CmdID:     cmdID,
		Status:    "ok",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Keys:      keys,
	}
	payload, err := json.Marshal(ack)
	if err != nil {
		slog.Error("marshal ack keys", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.mqtt.Publish(ctx, mqttclient.TopicAck(a.creds.DeviceID), payload, 1, false); err != nil {
		slog.Error("publish ack keys", "cmd_id", cmdID, "err", err)
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
	rawFiles := make(map[string]string)
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
		if raw, readErr := os.ReadFile("/etc/config/" + pkg); readErr == nil {
			rawFiles[pkg] = string(raw)
		}
	}

	state := StatePayload{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Trigger:   trigger,
		Packages:  packages,
		RawFiles:  rawFiles,
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

