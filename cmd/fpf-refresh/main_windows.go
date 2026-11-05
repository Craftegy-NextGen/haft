//go:build windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("fpf-refresh is not supported on Windows")
	os.Exit(1)
}