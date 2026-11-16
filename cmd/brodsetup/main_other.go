//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "brodsetup работает только в Windows")
	os.Exit(exitUsage)
}
