package main

import (
	"os"

	"framework2/internal/jiracli"
)

func main() {
	os.Exit(jiracli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
