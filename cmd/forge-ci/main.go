package main

import (
	"fmt"
	"os"

	"github.com/alexandremahdhaoui/forge-ci/internal/driver/clidriver"
	"github.com/alexandremahdhaoui/forge/pkg/enginecli"
)

var Version = "dev"

func main() {
	enginecli.Bootstrap(enginecli.Config{
		Name:    "forge-ci",
		Version: Version,
		RunCLI: func() error {
			return clidriver.New(os.Stdout).Run(os.Args[1:])
		},
		FailureHandler: func(err error) {
			fmt.Fprintf(os.Stderr, "forge-ci: %v\n", err)
		},
	})
}
