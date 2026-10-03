//go:build unix

package main

import (
	"os"
	"runtime"
	"syscall"
)

// mmapFile maps a whole file read-only. What is sliced out of it (.snd waves,
// cached sprite texels) is then file-backed pages the kernel can drop and
// re-read under memory pressure, instead of a heap copy: an HD pack's
// common.snd alone is ~90MiB.
func mmapFile(f *os.File) *fileMapping {
	fi, err := f.Stat()
	if err != nil || fi.Size() <= 0 || int64(int(fi.Size())) != fi.Size() {
		return nil
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(fi.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil
	}
	m := &fileMapping{data}
	runtime.SetFinalizer(m, func(m *fileMapping) { syscall.Munmap(m.data) })
	return m
}
