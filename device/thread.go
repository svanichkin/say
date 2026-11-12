package device

import "runtime"

// AudioThread keeps the calling goroutine on an OS thread of its own and
// raises that thread to the priority the system gives audio (raiseThread),
// for goroutines that move audio between the network and the devices: on
// a loaded machine a thread of ordinary priority waits for the CPU long
// enough to break the sound. The goroutine stays on the thread for life.
func AudioThread() {
	runtime.LockOSThread()
	raiseThread()
}
