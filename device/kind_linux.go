//go:build linux

package device

import (
	"os"
	"path/filepath"
	"strings"
)

// hasBattery: a battery among the power supplies, or a portable chassis
// (DMI: laptop, notebook, sub-notebook, convertible, detachable...).
func hasBattery() bool {
	if b, err := os.ReadFile("/sys/class/dmi/id/chassis_type"); err == nil {
		switch strings.TrimSpace(string(b)) {
		case "8", "9", "10", "14", "30", "31", "32":
			return true
		}
	}
	matches, _ := filepath.Glob("/sys/class/power_supply/*/type")
	for _, m := range matches {
		if b, err := os.ReadFile(m); err == nil && strings.TrimSpace(string(b)) == "Battery" {
			if scope, err := os.ReadFile(filepath.Join(filepath.Dir(m), "scope")); err == nil && strings.TrimSpace(string(scope)) == "Device" {
				continue // a mouse's or a headset's
			}
			return true
		}
	}
	return false
}
