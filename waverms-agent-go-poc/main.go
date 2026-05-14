package main

import (
    "fmt"
    "runtime"
)

var Version = "poc-0.1"

func main() {
    fmt.Printf("WaveRMS Agent Go PoC\n")
    fmt.Printf("Version:  %s\n", Version)
    fmt.Printf("GOOS:     %s\n", runtime.GOOS)
    fmt.Printf("GOARCH:   %s\n", runtime.GOARCH)
}