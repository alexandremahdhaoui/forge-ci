package terraformadapter

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
)

const binary = "terraform"

type Root struct {
	execPath string
}

func New() (Root, error) {
	execPath, err := exec.LookPath(binary)
	if err != nil {
		return Root{}, fmt.Errorf("looking up the %s binary on the path: %w", binary, err)
	}

	return Root{execPath: execPath}, nil
}

func (r Root) Init(ctx context.Context, dir string) error {
	tf, err := r.open(dir)
	if err != nil {
		return err
	}

	if err := tf.Init(ctx); err != nil {
		return fmt.Errorf("initializing the root module at %s: %w", dir, err)
	}

	return nil
}

func (r Root) Plan(ctx context.Context, dir string) (*tfjson.Plan, error) {
	tf, err := r.open(dir)
	if err != nil {
		return nil, err
	}

	held, err := os.MkdirTemp("", "forge-ci-plan")
	if err != nil {
		return nil, fmt.Errorf("making a directory for the plan of the root module at %s: %w", dir, err)
	}

	defer func() { _ = os.RemoveAll(held) }()

	path := filepath.Join(held, "plan")

	if _, err := tf.Plan(ctx, tfexec.Out(path)); err != nil {
		return nil, fmt.Errorf("planning the root module at %s: %w", dir, err)
	}

	plan, err := tf.ShowPlanFile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("reading the plan of the root module at %s: %w", dir, err)
	}

	return plan, nil
}

func (r Root) Apply(ctx context.Context, dir string) error {
	tf, err := r.open(dir)
	if err != nil {
		return err
	}

	if err := tf.Apply(ctx); err != nil {
		return fmt.Errorf("applying the root module at %s: %w", dir, err)
	}

	return nil
}

func (r Root) open(dir string) (*tfexec.Terraform, error) {
	tf, err := tfexec.NewTerraform(dir, r.execPath)
	if err != nil {
		return nil, fmt.Errorf("opening the root module at %s with %s: %w", dir, r.execPath, err)
	}

	return tf, nil
}
