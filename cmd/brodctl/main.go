// brodctl — командная строка для демона brodd.
package main

import (
	"os"

	"brod/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.Env{Out: os.Stdout, Err: os.Stderr}))
}
