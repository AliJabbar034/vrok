package awake

import (
	"runtime"
	"syscall"
)

const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
)

var setThreadExecutionState = syscall.NewLazyDLL("kernel32.dll").NewProc("SetThreadExecutionState")

// hold asks Windows to stay awake. The request belongs to the calling
// thread, so it is made from a goroutine locked to its own thread, which
// clears it again on release.
func hold() (func(), error) {
	if err := setThreadExecutionState.Find(); err != nil {
		return nil, ErrUnsupported
	}
	done := make(chan struct{})
	started := make(chan error, 1)
	go func() {
		// Never unlocked: if this goroutine ends, the thread is discarded
		// and the request with it.
		runtime.LockOSThread()
		if r, _, err := setThreadExecutionState.Call(esContinuous | esSystemRequired); r == 0 {
			started <- err
			return
		}
		started <- nil
		<-done
		setThreadExecutionState.Call(esContinuous)
	}()
	if err := <-started; err != nil {
		return nil, err
	}
	return func() { close(done) }, nil
}
