package device

import (
	"syscall"
	"unsafe"
)

var (
	avrt              = syscall.NewLazyDLL("avrt.dll")
	avSetMmThreadChar = avrt.NewProc("AvSetMmThreadCharacteristicsW")
	kernel32Thread    = syscall.NewLazyDLL("kernel32.dll")
	setThreadPriority = kernel32Thread.NewProc("SetThreadPriority")
)

// raiseThread registers the thread with the multimedia class scheduler as
// an audio thread (MMCSS "Audio", as audio engines do), or else gives it
// the highest ordinary priority.
func raiseThread() {
	if avSetMmThreadChar.Find() == nil {
		task, _ := syscall.UTF16PtrFromString("Audio")
		var index uint32
		if h, _, _ := avSetMmThreadChar.Call(uintptr(unsafe.Pointer(task)), uintptr(unsafe.Pointer(&index))); h != 0 {
			return
		}
	}
	if setThreadPriority.Find() == nil {
		const currentThread = ^uintptr(1) // GetCurrentThread's pseudo handle (-2)
		const highest = 2                 // THREAD_PRIORITY_HIGHEST
		setThreadPriority.Call(currentThread, highest)
	}
}
