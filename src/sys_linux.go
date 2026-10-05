package main

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

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
}

// threadRusage is what the calling thread has used so far: major page faults
// (disk reads), involuntary context switches, CPU time in and out of the kernel.
func threadRusage() (faults, preempt int64, user, sys time.Duration) {
	var ru syscall.Rusage
	syscall.Getrusage(1 /* RUSAGE_THREAD */, &ru)
	return int64(ru.Majflt), int64(ru.Nivcsw),
		time.Duration(syscall.TimevalToNsec(ru.Utime)), time.Duration(syscall.TimevalToNsec(ru.Stime))
}

// diskFree is the space left to this process on the partition holding dir,
// in bytes; negative when unknown.
func diskFree(dir string) int64 {
	var st syscall.Statfs_t
	if syscall.Statfs(dir, &st) != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// mayRotate is false when the kernel says the disk under f has no moving part
// (an SD card, an SSD) or can be pulled out: a USB stick, which USB storage
// calls rotational like the hard disk it is told apart from here. True
// settles nothing.
func mayRotate(f *os.File) bool {
	var st syscall.Stat_t
	if syscall.Fstat(int(f.Fd()), &st) != nil {
		return true
	}
	dev := fmt.Sprintf("/sys/dev/block/%d:%d/", st.Dev>>8&0xfff|st.Dev>>32&^0xfff, st.Dev&0xff|st.Dev>>12&^0xff)
	for _, disk := range []string{dev, dev + "../"} { // a whole disk, a partition
		if rot, err := os.ReadFile(disk + "queue/rotational"); err == nil {
			rem, _ := os.ReadFile(disk + "removable")
			return !bytes.HasPrefix(rot, []byte("0")) && !bytes.HasPrefix(rem, []byte("1"))
		}
	}
	return true
}

// lowPriority gives the calling goroutine's thread nice 10 for the rest of
// the goroutine: background loading must not take the CPU the game and
// frontend threads need for 60fps on a 4-core Pi. Locked, the thread is
// thrown away when the goroutine ends, so no other goroutine inherits it.
func lowPriority() {
	runtime.LockOSThread()
	syscall.Setpriority(syscall.PRIO_PROCESS, syscall.Gettid(), 10)
}
