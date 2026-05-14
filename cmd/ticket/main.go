package main

import (
	"os"

	"framework2/internal/ticketcli"
)

func main() {
	os.Exit(ticketcli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
