//go:build !unix

package main

import "os"

func mmapFile(*os.File) *fileMapping { return nil }
