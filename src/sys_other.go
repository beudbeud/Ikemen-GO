//go:build !linux

package main

import (
	"os"
	"time"
)

func willNeed([]byte) {}

func lowPriority() {}

func populate([]byte) {}

func diskFree(string) int64 { return -1 }

// No populate here: nothing to gain from knowing.
func mayRotate(*os.File) bool { return false }

func threadRusage() (faults, preempt int64, user, sys time.Duration) { return }
