package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/docscontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/types/docstypes"
	"sigs.k8s.io/yaml"
)

const (
	schemaPath    = ".forge/spec-cache/forge-ci.v1.yaml"
	decisionsPath = "docs/decisions.yaml"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "docgen: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	fs := fsadapter.New()

	files, err := collect(fs)
	if err != nil {
		return err
	}

	for _, f := range files {
		if err := fs.WriteFile(f.Path, []byte(f.Content)); err != nil {
			return err
		}

		fmt.Printf("docgen: wrote %s\n", f.Path)
	}

	return nil
}

func collect(fs fsadapter.FS) ([]docstypes.File, error) {
	var out []docstypes.File

	specs, err := filepath.Glob(filepath.Join("cmd", "ci-*", "spec.yaml"))
	if err != nil {
		return nil, fmt.Errorf("looking for engine specs: %w", err)
	}

	if len(specs) == 0 {
		return nil, fmt.Errorf("no engine spec found under cmd/ci-*/spec.yaml")
	}

	for _, path := range specs {
		raw, err := fs.ReadFile(path)
		if err != nil {
			return nil, err
		}

		var engine docstypes.Engine
		if err := yaml.UnmarshalStrict(raw, &engine); err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}

		generated, err := docscontroller.Engine(engine)
		if err != nil {
			return nil, err
		}

		out = append(out, generated...)
	}

	schema, err := fs.ReadFile(schemaPath)
	if err != nil {
		return nil, err
	}

	concepts, err := docscontroller.Concepts(schema, schemaPath)
	if err != nil {
		return nil, err
	}

	out = append(out, concepts)

	raw, err := fs.ReadFile(decisionsPath)
	if err != nil {
		return nil, err
	}

	var decisions docstypes.Decisions
	if err := yaml.UnmarshalStrict(raw, &decisions); err != nil {
		return nil, fmt.Errorf("reading %s: %w", decisionsPath, err)
	}

	return append(out, docscontroller.Decisions(decisions, decisionsPath)), nil
}
