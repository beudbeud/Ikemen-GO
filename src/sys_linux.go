package main

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

func gettid() int { return syscall.Gettid() }

// willNeed starts reading a whole mapping into the page cache in the
// background, so that a later first touch does not wait on the disk.
func willNeed(b []byte) { syscall.Madvise(b, syscall.MADV_WILLNEED) }

// populate reads a mapped range in now, in one system call
// (MADV_POPULATE_READ, Linux 5.14+). Touching the pages instead left the
// goroutine in page faults off a USB stick, where it cannot stop: every
// stop-the-world -- GC phases -- then waited on it, freezing the game
// thread for tens of ms. A goroutine in a syscall does not hold that up.
func populate(b []byte) {
	if len(b) == 0 {
		return
	}
	ps := uintptr(os.Getpagesize())
	start := uintptr(unsafe.Pointer(&b[0]))
	base := start &^ (ps - 1)
	syscall.Syscall(syscall.SYS_MADVISE, base, start+uintptr(len(b))-base, 22 /* MADV_POPULATE_READ */)
	runtime.KeepAlive(b)
}

// lowPriority gives the calling goroutine's thread nice 10 for the rest of
// the goroutine: background loading must not take the CPU the game and
// frontend threads need for 60fps on a 4-core Pi. Locked, the thread is
// thrown away when the goroutine ends, so no other goroutine inherits it.
func lowPriority() {
	runtime.LockOSThread()
	syscall.Setpriority(syscall.PRIO_PROCESS, syscall.Gettid(), 10)
}
