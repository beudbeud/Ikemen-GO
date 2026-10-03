package main

import (
	"runtime"
	"syscall"
)

func gettid() int { return syscall.Gettid() }

// willNeed starts reading a whole mapping into the page cache in the
// background, so that a later first touch does not wait on the disk.
func willNeed(b []byte) { syscall.Madvise(b, syscall.MADV_WILLNEED) }

// lowPriority gives the calling goroutine's thread nice 10 for the rest of
// the goroutine: background loading must not take the CPU the game and
// frontend threads need for 60fps on a 4-core Pi. Locked, the thread is
// thrown away when the goroutine ends, so no other goroutine inherits it.
func lowPriority() {
	runtime.LockOSThread()
	syscall.Setpriority(syscall.PRIO_PROCESS, syscall.Gettid(), 10)
}
