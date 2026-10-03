package main

import "syscall"

func gettid() int { return syscall.Gettid() }
