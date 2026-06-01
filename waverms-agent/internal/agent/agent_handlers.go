package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OliverKruecken/waverms-agent/internal/apply"
)

func (a *Agent) handleUCISet(cmd Command) {
	var uciPayload UCICommandPayload
	if err := json.Unmarshal(cmd.Payload, &uciPayload); err != nil {
		slog.Error("invalid uci_set payload", "cmd_id", cmd.CmdID, "err", err)
		a.publishAck(cmd.CmdID, "error", fmt.Sprintf("invalid payload: %v", err))
		return
	}

	var output strings.Builder
	status := "ok"
	for _, rawCmd := range uciPayload.Commands {
		// Strip leading "uci " prefix if present.
		args := strings.Fields(strings.TrimPrefix(rawCmd, "uci "))
		if len(args) == 0 {
			continue
		}
		// Allowlist the subcommand to prevent injection of destructive
		// UCI operations (e.g. "batch", "import") if the broker is
		// compromised or a message is replayed.
		if !allowedUCISubcmds[args[0]] {
			slog.Error("uci_set rejected: subcommand not in allowlist", "cmd_id", cmd.CmdID, "subcommand", args[0])
			output.WriteString(fmt.Sprintf("error: uci subcommand not permitted: %s\n", args[0]))
			status = "error"
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
	a.publishAck(cmd.CmdID, status, strings.TrimSpace(output.String()))
}

func (a *Agent) handleConfigApply(cmd Command) {
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
	if err := a.publishState(ctx, "apply_success", committed); err != nil {
		slog.Error("publish state after config_apply", "err", err)
	}
	cancel()
}

func (a *Agent) handleFileWrite(cmd Command) {
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
	// Reject paths outside the allowlist to prevent arbitrary file writes.
	// filepath.Clean collapses traversal sequences before the prefix check.
	if !isFileWritePathAllowed(fwPayload.Path) {
		slog.Error("file_write rejected: path not in allowlist", "cmd_id", cmd.CmdID, "path", fwPayload.Path)
		a.publishAck(cmd.CmdID, "error", fmt.Sprintf("path not permitted: %s", fwPayload.Path))
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
	// Decode content according to format.
	// "dropbear": content is standard base64 (RFC 4648) for raw binary files.
	// "openssh" or absent: content is a plain UTF-8 string (backward-compatible default).
	var fileContent []byte
	switch fwPayload.Format {
	case "dropbear":
		decoded, err := base64.StdEncoding.DecodeString(fwPayload.Content)
		if err != nil {
			slog.Error("file_write: base64 decode failed", "cmd_id", cmd.CmdID, "err", err)
			a.publishAck(cmd.CmdID, "error", fmt.Sprintf("base64 decode: %v", err))
			return
		}
		fileContent = decoded
	default: // "openssh" or absent — plain text, backward-compatible
		fileContent = []byte(fwPayload.Content)
	}
	if err := a.fileWriter.WriteFile(fwPayload.Path, fileContent, perm); err != nil {
		slog.Error("file_write failed", "cmd_id", cmd.CmdID, "path", fwPayload.Path, "err", err)
		a.publishAck(cmd.CmdID, "error", fmt.Sprintf("write %s: %v", fwPayload.Path, err))
		return
	}
	slog.Info("file_write succeeded", "cmd_id", cmd.CmdID, "path", fwPayload.Path)
	a.publishAck(cmd.CmdID, "ok", "")
}

func (a *Agent) handleHostKeyRestore(cmd Command) {
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
	allowedFilenames := allowedHostKeyFilenamesByDaemon[a.sshDaemon.Name]
	for name, content := range hkPayload.Keys {
		// filepath.Base strips directory components to prevent path traversal.
		safe := filepath.Base(name)
		// Allowlist check: only filenames valid for the detected SSH daemon are accepted.
		if !allowedFilenames[safe] {
			slog.Error("host_key_restore: filename not in allowlist", "cmd_id", cmd.CmdID, "file", safe, "daemon", a.sshDaemon.Name)
			a.publishAck(cmd.CmdID, "error", fmt.Sprintf("filename not permitted for %s: %s", a.sshDaemon.Name, safe))
			return
		}
		path := a.sshDaemon.Dir + safe
		if err := a.fileWriter.WriteFile(path, content, 0600); err != nil {
			slog.Error("host_key_restore: write failed", "cmd_id", cmd.CmdID, "file", safe, "err", err)
			a.publishAck(cmd.CmdID, "error", fmt.Sprintf("write %s: %v", safe, err))
			return
		}
		slog.Debug("host_key_restore: wrote key file", "file", safe)
	}
	// Restart the SSH daemon so it picks up the restored keys.
	if _, err := a.uci.ExecCmd(a.sshDaemon.Service, "restart"); err != nil {
		// Non-fatal: keys are already on disk; the daemon will use them on next connection.
		slog.Warn("host_key_restore: service restart failed (non-fatal)", "cmd_id", cmd.CmdID, "daemon", a.sshDaemon.Name, "err", err)
	}
	slog.Info("host_key_restore succeeded", "cmd_id", cmd.CmdID, "files", len(hkPayload.Keys))
	a.publishAck(cmd.CmdID, "ok", "")
}

func (a *Agent) handleHostKeyRemove(cmd Command) {
	removed := 0
	for name := range allowedHostKeyFilenamesByDaemon[a.sshDaemon.Name] {
		path := a.sshDaemon.Dir + name
		err := os.Remove(path)
		if err != nil && !os.IsNotExist(err) {
			slog.Warn("host_key_remove: could not remove file", "cmd_id", cmd.CmdID, "path", path, "err", err)
		} else if err == nil {
			slog.Debug("host_key_remove: removed key file", "path", path)
			removed++
		}
	}
	if _, err := a.uci.ExecCmd(a.sshDaemon.Service, "restart"); err != nil {
		// Non-fatal: keys are already gone; the daemon will regenerate on next start.
		slog.Warn("host_key_remove: service restart failed (non-fatal)", "cmd_id", cmd.CmdID, "daemon", a.sshDaemon.Name, "err", err)
	}
	slog.Info("host_key_remove succeeded", "cmd_id", cmd.CmdID, "removed", removed)
	a.publishAck(cmd.CmdID, "ok", "")
}

type setPasswordPayload struct {
	Target string `json:"target"`
	Hash   string `json:"hash"`
}

func (a *Agent) handleSetPassword(cmd Command) {
	var p setPasswordPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		slog.Error("invalid set_password payload", "cmd_id", cmd.CmdID, "err", err)
		a.publishAck(cmd.CmdID, "error", fmt.Sprintf("invalid payload: %v", err))
		return
	}
	if p.Target != "root" {
		slog.Error("set_password rejected: unsupported target", "cmd_id", cmd.CmdID, "target", p.Target)
		a.publishAck(cmd.CmdID, "error", fmt.Sprintf("unsupported target: %s", p.Target))
		return
	}
	if p.Hash == "" {
		a.publishAck(cmd.CmdID, "error", "hash is required")
		return
	}
	if err := a.passwordSetter.SetPassword(p.Target, p.Hash); err != nil {
		slog.Error("set_password failed", "cmd_id", cmd.CmdID, "target", p.Target, "err", err)
		a.publishAck(cmd.CmdID, "error", err.Error())
		return
	}
	slog.Info("set_password succeeded", "cmd_id", cmd.CmdID, "target", p.Target)
	a.publishAck(cmd.CmdID, "ok", "")
}

func (a *Agent) handleHostKeyFetch(cmd Command) {
	allowedFilenames := allowedHostKeyFilenamesByDaemon[a.sshDaemon.Name]
	keys := make(map[string][]byte)
	for name := range allowedFilenames {
		path := a.sshDaemon.Dir + name
		content, err := a.fileWriter.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			slog.Error("host_key_fetch: read failed", "cmd_id", cmd.CmdID, "file", name, "err", err)
			a.publishAck(cmd.CmdID, "error", fmt.Sprintf("read %s: %v", name, err))
			return
		}
		keys[name] = content
	}
	if len(keys) == 0 {
		slog.Warn("host_key_fetch: no host key files found", "cmd_id", cmd.CmdID, "daemon", a.sshDaemon.Name)
		a.publishAck(cmd.CmdID, "error", "no host key files found")
		return
	}
	slog.Info("host_key_fetch succeeded", "cmd_id", cmd.CmdID, "files", len(keys))
	a.publishAckKeys(cmd.CmdID, keys)
}
