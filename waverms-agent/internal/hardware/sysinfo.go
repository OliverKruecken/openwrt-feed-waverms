package hardware

import (
	"os"
	"strings"
)

// GetOpenWrtVersion reads the release version from /etc/openwrt_release.
// Returns "unknown" if the file is missing or the field is not found.
func GetOpenWrtVersion() string {
	return getOpenWrtVersion("/etc/openwrt_release")
}

func getOpenWrtVersion(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "DISTRIB_RELEASE=") {
			continue
		}
		val := strings.TrimPrefix(line, "DISTRIB_RELEASE=")
		val = strings.Trim(val, `"'`)
		if val != "" {
			return val
		}
	}
	return "unknown"
}

// GetModel reads the hardware model string from /tmp/sysinfo/model (OpenWrt standard).
// Returns "unknown" if the file is missing or empty.
func GetModel() string {
	return getModel("/tmp/sysinfo/model")
}

func getModel(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	model := strings.TrimSpace(string(data))
	if model == "" {
		return "unknown"
	}
	return model
}
