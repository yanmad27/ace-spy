//go:build !windows

package main

import "log"

func main() { log.Fatal("atlasgen renders with Windows GDI; run it on Windows") }
