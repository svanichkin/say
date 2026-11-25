//go:build windows

package device

import (
	"syscall"
	"unsafe"
)

var procGetSystemPowerStatus = syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemPowerStatus")

// hasBattery: GetSystemPowerStatus says there is a system battery.
func hasBattery() bool {
	var st struct {
		ACLineStatus, BatteryFlag, BatteryLifePercent, SystemStatusFlag uint8
		BatteryLifeTime, BatteryFullLifeTime                            uint32
	}
	if r, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&st))); r == 0 {
		return false
	}
	return st.BatteryFlag != 128 && st.BatteryFlag != 255 // 128: no battery, 255: unknown
}
