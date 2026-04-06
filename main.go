package main

import (
	"fmt"
	"os"

	"github.com/antoniocascais/motoko/cmd"
)

var version = "dev"

func main() {
	cmd.Version = version
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
