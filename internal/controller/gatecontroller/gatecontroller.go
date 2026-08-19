package gatecontroller

import (
	"fmt"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

type Controller struct {
	fs    fsadapter.FS
	alias string
}

func New(fs fsadapter.FS, alias string) *Controller {
	return &Controller{fs: fs, alias: alias}
}

func (c *Controller) Declare(map[string]any) (citypes.DeclareOutput, error) {
	return citypes.DeclareOutput{Resources: []citypes.Resource{}}, nil
}

func (c *Controller) Evaluate(in citypes.GateInput) (citypes.GateResult, error) {
	result := citypes.GateResult{Alias: c.alias}

	if in.Run.Status == citypes.StatusFailed {
		result.Status = citypes.StatusFailed
		result.Message = "the substage failed, so there is nothing to approve"

		return result, nil
	}

	path, _ := in.Spec["approvalPath"].(string)
	if strings.TrimSpace(path) == "" {
		result.Status = citypes.StatusPending
		result.Message = "waiting for approval. set spec.approvalPath and create that file to approve"

		return result, nil
	}

	approved, err := c.fs.Exists(path)
	if err != nil {
		return citypes.GateResult{}, fmt.Errorf("checking approval at %s: %w", path, err)
	}

	if approved {
		result.Status = citypes.StatusPassed
		result.Message = "approved by " + path

		return result, nil
	}

	result.Status = citypes.StatusPending
	result.Message = "waiting for approval. create " + path + " to approve"

	return result, nil
}
