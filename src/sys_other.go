//go:build !linux

package main

import "time"

func willNeed([]byte) {}

func lowPriority() {}

func populate([]byte) {}

func diskFree(string) int64 { return -1 }

func threadRusage() (faults, preempt int64, user, sys time.Duration) { return }
