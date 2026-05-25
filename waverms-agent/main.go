package main

import (
	"context"
	"crypto/tls"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"syscall"

	"github.com/OliverKruecken/waverms-agent/internal/agent"
	"github.com/OliverKruecken/waverms-agent/internal/config"
	"github.com/OliverKruecken/waverms-agent/internal/hardware"
	mqttclient "github.com/OliverKruecken/waverms-agent/internal/mqtt"
	"github.com/OliverKruecken/waverms-agent/internal/uci"
)

// Version is set at build time by -ldflags "-X main.Version=...".
var Version = "1.0.0"

func main() {
	// On embedded targets GOMAXPROCS defaults to the number of CPU cores, which
	// causes the Go runtime to reserve proportionally more virtual address space
	// and spin up more GC workers. Single-threading the scheduler keeps VSZ in
	// check on devices with tight memory.
	runtime.GOMAXPROCS(1)

	// GOMEMLIMIT caps the soft heap target so the GC runs more aggressively
	// before the OOM killer steps in. The default (math.MaxInt64) is fine on
	// servers but wrong for embedded. Honour the env var if set; otherwise
	// default to 32 MiB, which is enough for normal operation.
	if limitStr := os.Getenv("GOMEMLIMIT"); limitStr != "" {
		if v, err := strconv.ParseInt(limitStr, 10, 64); err == nil {
			debug.SetMemoryLimit(v)
		}
	} else {
		debug.SetMemoryLimit(32 << 20) // 32 MiB
	}

	cfg, err := config.Load("/etc/waverms/config")
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	cfg.AgentVersion = Version

	logLevel := slog.LevelInfo
	if cfg.Debug {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))
	log.SetFlags(0) // slog handler owns timestamps

	creds, err := config.LoadCredentials("/etc/waverms/credentials")
	if err != nil && !os.IsNotExist(err) {
		log.Fatalf("credentials: %v", err)
	}

	mac, err := hardware.GetFirstPhysicalMAC()
	if err != nil {
		log.Printf("warning: cannot determine MAC address: %v", err)
	}

	var tlsCfg *tls.Config
	if cfg.TLSInsecure {
		tlsCfg = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	} else {
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS13}
	}

	a := agent.New(&agent.Options{
		Config:         cfg,
		Creds:          creds,
		MAC:            mac,
		Model:          hardware.GetModel(),
		OpenWrtVersion: hardware.GetOpenWrtVersion(),
		MQTT:           mqttclient.NewPahoClient(tlsCfg),
		UCI:            &uci.RealUCIRunner{},
		Version:        Version,
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := a.Run(ctx); err != nil {
		log.Fatalf("agent: %v", err)
	}
}
