//go:build unix

package main

import (
	"os"
	"runtime"
	"syscall"
)

// mmapSnd maps a whole .snd file read-only. Its waves are then file-backed
// pages the kernel can drop and re-read under memory pressure, instead of a
// heap copy: an HD pack's common.snd alone is ~90MiB.
func mmapSnd(f *os.File) *sndMapping {
	fi, err := f.Stat()
	if err != nil || fi.Size() <= 0 || int64(int(fi.Size())) != fi.Size() {
		return nil
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(fi.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil
	}
	m := &sndMapping{data}
	runtime.SetFinalizer(m, func(m *sndMapping) { syscall.Munmap(m.data) })
	return m
}
