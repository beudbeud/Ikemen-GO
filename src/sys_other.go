//go:build !linux

package main

// gettid is unknown here: callers treat 0 as "can't tell which thread".
func gettid() int { return 0 }

func willNeed([]byte) {}

func lowPriority() {}
