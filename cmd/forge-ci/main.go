package main

import (
	"context"
	"fmt"
	"os"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/engineadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/reconcilecontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/driver/clidriver"
	"github.com/alexandremahdhaoui/forge/pkg/enginecli"
	"github.com/alexandremahdhaoui/forge/pkg/engineversion"
)

var Version = "dev"

func main() {
	sourceDir := os.Getenv("FORGE_CI_SOURCE_DIR")

	// The effective version pins engine go-run fallbacks: a go-install build
	// carries its module version in build info even when ldflags stamped
	// nothing, so every engine matches the CLI that spawned it.
	caller := engineadapter.NewMCPCaller(sourceDir, engineversion.GetEffectiveVersion(Version), os.Stderr)
	git := gitadapter.New(execadapter.New())
	reconciler := reconcilecontroller.New(caller, git, nil)

	enginecli.Bootstrap(enginecli.Config{
		Name:    "forge-ci",
		Version: Version,
		RunCLI: func() error {
			applying := os.Getenv(clidriver.EnvInApply) != ""

			if err := os.Setenv(clidriver.EnvInApply, "1"); err != nil {
				return fmt.Errorf("marking the apply in progress: %w", err)
			}

			return clidriver.New(os.Stdout, reconciler).
				AlreadyApplying(applying).
				Run(context.Background(), os.Args[1:])
		},
		FailureHandler: func(err error) {
			fmt.Fprintf(os.Stderr, "forge-ci: %v\n", err)
		},
	})
}
