//go:build !linux

// brod-helper нужен только на Linux: на других платформах права демону выдаёт их установщик.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "brod-helper работает только на Linux")
	os.Exit(2)
}
