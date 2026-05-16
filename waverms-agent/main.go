package main

import (
	"context"
	"crypto/tls"
	"log"
	"log/slog"
	"os"
	"os/signal"
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
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
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
