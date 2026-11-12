package device

/*
#include <pthread.h>
#include <pthread/qos.h>

static int sayRaiseThread(void) {
	return pthread_set_qos_class_self_np(QOS_CLASS_USER_INTERACTIVE, 0);
}
*/
import "C"

// raiseThread puts the thread in the highest quality-of-service class, the
// one of the user interface and of audio that is not real-time.
func raiseThread() { C.sayRaiseThread() }
