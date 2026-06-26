package agent

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OliverKruecken/waverms-agent/internal/apply"
)

// connectivityWatchdogTimeout is how long the agent waits for a confirmed
// MQTT connection after a config_apply before treating the apply as successful.
// Overridden in tests to a short value.
var connectivityWatchdogTimeout = 2 * 60 * time.Second

// uciConfigDir is the directory that holds committed UCI config files.
// Overridden in tests to a temporary directory.
var uciConfigDir = "/etc/config"

// rollbackAck is a deferred ACK that must be sent on the next MQTT session
// after the agent rolled back a config_apply due to connectivity loss.
type rollbackAck struct {
	cmdID  string
	status string
	output string
}

// configBackup holds a snapshot of /etc/config/{pkg} for each affected package,
// taken before a config_apply. A nil entry means the package did not exist.
type configBackup struct {
	data map[string][]byte // pkg → raw file content (nil = did not exist)
}

// backupConfigFiles reads the current /etc/config/{pkg} for each package and
// returns a configBackup. A package that does not yet exist is recorded as nil
// so that restore() can remove it if rollback is needed.
func backupConfigFiles(packages []string) (configBackup, error) {
	b := configBackup{data: make(map[string][]byte, len(packages))}
	for _, pkg := range packages {
		content, err := os.ReadFile(filepath.Join(uciConfigDir, pkg))
		if os.IsNotExist(err) {
			b.data[pkg] = nil
			continue
		}
		if err != nil {
			return configBackup{}, fmt.Errorf("backup %s: %w", pkg, err)
		}
		b.data[pkg] = content
	}
	return b, nil
}

// restore writes the backed-up config files back to uciConfigDir and
// reloads the affected services. Errors are logged but do not abort the
// restore loop — we attempt every package even if one fails.
func (b configBackup) restore(packages []string) error {
	var errs []string
	for _, pkg := range packages {
		path := filepath.Join(uciConfigDir, pkg)
		content, hadContent := b.data[pkg]
		if !hadContent || content == nil {
			// Package did not exist before the apply — remove it.
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Sprintf("remove %s: %v", pkg, err))
			}
			continue
		}
		if err := os.WriteFile(path, content, 0644); err != nil {
			errs = append(errs, fmt.Sprintf("restore %s: %v", pkg, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("restore: %s", strings.Join(errs, "; "))
	}
	// Restart affected services so they pick up the restored config.
	if reloadErrs := apply.RunReloads(packages); len(reloadErrs) > 0 {
		slog.Warn("rollback: service reload failures", "errors", strings.Join(reloadErrs, "; "))
	}
	return nil
}

// runConnectivityWatchdog is the commit-confirm watchdog for config_apply.
//
// After a successful apply, this goroutine waits for connectivityWatchdogTimeout.
// If the server sends config_confirm (confirmCh is closed) the apply is confirmed
// immediately. If disconnCh fires first the backup is restored and a rollback ACK
// is queued. If neither fires within the timeout the apply is confirmed by timeout.
func (a *Agent) runConnectivityWatchdog(cmdID string, backup configBackup, packages []string, disconnCh <-chan struct{}, confirmCh <-chan struct{}) {
	defer a.clearWatchdogConfirm(cmdID)

	if disconnCh == nil {
		// Session vanished between apply and watchdog start — roll back immediately.
		slog.Warn("config_apply watchdog: no active session, rolling back", "cmd_id", cmdID)
		a.doRollback(cmdID, backup, packages)
		return
	}

	timer := time.NewTimer(connectivityWatchdogTimeout)
	defer timer.Stop()

	select {
	case <-confirmCh:
		slog.Info("config_apply confirmed by server", "cmd_id", cmdID)
		a.publishAck(cmdID, "ok", "")
	case <-timer.C:
		slog.Info("config_apply connectivity confirmed", "cmd_id", cmdID, "after", connectivityWatchdogTimeout)
		a.publishAck(cmdID, "ok", "")
	case <-disconnCh:
		slog.Warn("config_apply connectivity lost during watchdog, rolling back", "cmd_id", cmdID)
		a.doRollback(cmdID, backup, packages)
	}
}

// doRollback restores the pre-apply config and queues a deferred ACK error.
func (a *Agent) doRollback(cmdID string, backup configBackup, packages []string) {
	if err := backup.restore(packages); err != nil {
		slog.Error("rollback restore failed", "cmd_id", cmdID, "err", err)
	}
	a.setPendingRollbackAck(&rollbackAck{
		cmdID:  cmdID,
		status: "error",
		output: "connectivity lost after config apply – rolled back to previous config",
	})
}
