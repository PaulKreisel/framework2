package main

import (
	"os"

	"framework2/internal/githubcli"
)

func main() {
	os.Exit(githubcli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
