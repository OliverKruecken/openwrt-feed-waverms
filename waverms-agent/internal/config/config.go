// Package config handles reading and writing agent configuration files.
// Config files use a simple key=value format, one per line, with # for comments.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds the runtime configuration for the agent.
type Config struct {
	BrokerHost        string
	BrokerPort        int
	TLSInsecure       bool
	HeartbeatInterval int
	StateInterval     int
	AgentVersion      string
	Debug             bool
}

const (
	defaultBrokerPort        = 8883
	defaultHeartbeatInterval = 60
	defaultStateInterval     = 600
)

// Load reads the config file at path and returns a Config.
// Missing keys fall back to defaults. A missing file is not an error – defaults are used.
func Load(path string) (*Config, error) {
	cfg := &Config{
		BrokerPort:        defaultBrokerPort,
		HeartbeatInterval: defaultHeartbeatInterval,
		StateInterval:     defaultStateInterval,
		AgentVersion:      "1.0.0",
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("open config %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		// strip inline comment
		if idx := strings.Index(value, " #"); idx >= 0 {
			value = strings.TrimSpace(value[:idx])
		}

		switch key {
		case "BROKER_HOST":
			cfg.BrokerHost = value
		case "BROKER_PORT":
			if p, err := strconv.Atoi(value); err == nil {
				cfg.BrokerPort = p
			}
		case "TLS_INSECURE":
			cfg.TLSInsecure = strings.EqualFold(value, "true")
		case "HEARTBEAT_INTERVAL":
			if v, err := strconv.Atoi(value); err == nil {
				cfg.HeartbeatInterval = v
			}
		case "STATE_INTERVAL":
			if v, err := strconv.Atoi(value); err == nil {
				cfg.StateInterval = v
			}
		case "AGENT_VERSION":
			cfg.AgentVersion = value
		case "DEBUG":
			cfg.Debug = strings.EqualFold(value, "true")
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan config %s: %w", path, err)
	}

	return cfg, nil
}

// ReadBootstrapToken reads the one-line token file at path.
func ReadBootstrapToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
