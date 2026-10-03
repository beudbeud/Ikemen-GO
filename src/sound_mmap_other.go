//go:build !unix

package main

import "os"

func mmapSnd(*os.File) *sndMapping { return nil }
