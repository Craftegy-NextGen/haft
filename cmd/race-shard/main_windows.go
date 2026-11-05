//go:build windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("race-shard is not supported on Windows")
	os.Exit(1)
}