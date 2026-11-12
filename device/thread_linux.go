package device

import "syscall"

// raiseThread lowers the thread's nice value; without the right to (an
// ordinary user may only raise it) the thread stays as it is.
func raiseThread() {
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, syscall.Gettid(), -10)
}
