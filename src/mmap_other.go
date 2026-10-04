//go:build !unix

package main

import "os"

const canMmap = false

func mmapFile(*os.File) *fileMapping { return nil }
