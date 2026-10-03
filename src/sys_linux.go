package main

import "syscall"

func gettid() int { return syscall.Gettid() }

// willNeed starts reading a whole mapping into the page cache in the
// background, so that a later first touch does not wait on the disk.
func willNeed(b []byte) { syscall.Madvise(b, syscall.MADV_WILLNEED) }
