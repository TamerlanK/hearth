package main

import (
	"os"

	"github.com/TamerlanK/hearth/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
