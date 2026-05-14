package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Credentials holds the device credentials written by the bootstrap flow.
type Credentials struct {
	DeviceID   string
	Secret     string
	BrokerHost string
	BrokerPort int
}

// HasCredentials returns true when the credentials contain a non-empty DeviceID and Secret.
func (c *Credentials) HasCredentials() bool {
	return c != nil && c.DeviceID != "" && c.Secret != ""
}

// LoadCredentials reads credentials from path. Returns an empty Credentials (not an error)
// if the file does not exist – callers should check HasCredentials().
func LoadCredentials(path string) (*Credentials, error) {
	creds := &Credentials{}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return creds, nil
		}
		return nil, fmt.Errorf("open credentials %s: %w", path, err)
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
		case "DEVICE_ID":
			creds.DeviceID = value
		case "SECRET":
			creds.Secret = value
		case "BROKER_HOST":
			creds.BrokerHost = value
		case "BROKER_PORT":
			if p, err := strconv.Atoi(value); err == nil {
				creds.BrokerPort = p
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan credentials %s: %w", path, err)
	}

	return creds, nil
}

// SaveCredentials writes credentials to path (chmod 600).
func SaveCredentials(path, deviceID, secret, brokerHost string, brokerPort int) error {
	var b strings.Builder
	fmt.Fprintf(&b, "DEVICE_ID=%s\n", deviceID)
	fmt.Fprintf(&b, "SECRET=%s\n", secret)
	if brokerHost != "" {
		fmt.Fprintf(&b, "BROKER_HOST=%s\n", brokerHost)
	}
	if brokerPort > 0 {
		fmt.Fprintf(&b, "BROKER_PORT=%d\n", brokerPort)
	}

	if err := os.WriteFile(path, []byte(b.String()), 0600); err != nil {
		return fmt.Errorf("write credentials %s: %w", path, err)
	}
	return nil
}
