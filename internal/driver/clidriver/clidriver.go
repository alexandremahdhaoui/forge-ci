package clidriver

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
)

const DefaultPath = "pipeline.yaml"

var ErrUsage = errors.New("usage: forge-ci validate [--config path]")

type Driver struct {
	out io.Writer
}

func New(out io.Writer) *Driver {
	return &Driver{out: out}
}

func (d *Driver) Run(args []string) error {
	if len(args) == 0 {
		return ErrUsage
	}

	switch args[0] {
	case "validate":
		return d.validate(args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q: %w", args[0], ErrUsage)
	}
}

func (d *Driver) validate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(d.out)

	path := fs.String("config", DefaultPath, "path to the pipeline file")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parsing flags: %w", err)
	}

	p, err := Load(*path)
	if err != nil {
		return err
	}

	fmt.Fprintf(d.out, "%s: %d repos, %d engines, %d stages\n",
		p.Name, len(p.Repos), len(p.Engines), len(p.Stages))

	for i, s := range p.Stages {
		fmt.Fprintf(d.out, "  %d. %s (%d substages)\n", i+1, s.Name, len(s.Substages))
	}

	return nil
}

func Load(path string) (config.Pipeline, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return config.Pipeline{}, fmt.Errorf("reading %s: %w", path, err)
	}

	p, err := config.Parse(raw)
	if err != nil {
		return config.Pipeline{}, fmt.Errorf("validating %s: %w", path, err)
	}

	return p, nil
}
