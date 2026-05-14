package main

import (
	"os"

	"framework2/internal/thiscli"
)

func main() {
	os.Exit(thiscli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
