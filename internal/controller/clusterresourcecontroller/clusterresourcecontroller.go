package clusterresourcecontroller

import (
	"errors"
	"fmt"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	kindHelmRelease = "helm-release"
	kindSecret      = "secret"
)

var (
	ErrResources = errors.New(
		"this engine needs spec.resources naming at least one resource, each with a kind")
	ErrNeverPublishes = errors.New(
		"this engine declares cluster resources and never publishes: no substage may name it")
)

type Controller struct {
	fs fsadapter.FS
}

func New(fs fsadapter.FS) *Controller {
	return &Controller{fs: fs}
}

func (c *Controller) Declare(in citypes.DeclareInput) (citypes.DeclareOutput, error) {
	apiServer, err := requiredString(in.Spec, "apiServer")
	if err != nil {
		return citypes.DeclareOutput{}, err
	}

	declared, ok := in.Spec["resources"].([]any)
	if !ok || len(declared) == 0 {
		return citypes.DeclareOutput{}, ErrResources
	}

	resources := make([]citypes.Resource, 0, len(declared))

	for index, entry := range declared {
		held, ok := entry.(map[string]any)
		if !ok {
			return citypes.DeclareOutput{}, fmt.Errorf(
				"reading spec.resources[%d]: an object is required, the spec holds a %T", index, entry)
		}

		resource, err := c.declareOne(in.Root, index, held, apiServer)
		if err != nil {
			return citypes.DeclareOutput{}, err
		}

		resources = append(resources, resource)
	}

	return citypes.DeclareOutput{Resources: resources}, nil
}

func (c *Controller) Publish() error {
	return ErrNeverPublishes
}

func (c *Controller) declareOne(
	root string, index int, held map[string]any, apiServer string,
) (citypes.Resource, error) {
	kind, err := citypes.SpecString(held, "kind")
	if err != nil {
		return citypes.Resource{}, fmt.Errorf("reading spec.resources[%d]: %w", index, err)
	}

	switch kind {
	case kindHelmRelease:
		return c.helmRelease(root, index, held, apiServer)
	case kindSecret:
		return declaredSecret(index, held, apiServer)
	case "":
		return citypes.Resource{}, fmt.Errorf("reading spec.resources[%d]: kind is required", index)
	default:
		return citypes.Resource{}, fmt.Errorf(
			"reading spec.resources[%d]: kind %q is unknown, this engine declares %s and %s",
			index, kind, kindHelmRelease, kindSecret)
	}
}

func declaredSecret(index int, held map[string]any, apiServer string) (citypes.Resource, error) {
	namespace, err := entryString(index, held, "namespace")
	if err != nil {
		return citypes.Resource{}, err
	}

	name, err := entryString(index, held, "name")
	if err != nil {
		return citypes.Resource{}, err
	}

	id := namespace + "/" + name

	variables, err := citypes.SpecStringMap(held, "data")
	if err != nil {
		return citypes.Resource{}, fmt.Errorf("reading the data of secret %s: %w", id, err)
	}

	if len(variables) == 0 {
		return citypes.Resource{}, fmt.Errorf(
			"reading the data of secret %s: data is required, and it names one environment variable per key",
			id)
	}

	data := make(map[string]any, len(variables))

	for key, variable := range variables {
		if key == "" {
			return citypes.Resource{}, fmt.Errorf(
				"reading the data of secret %s: data holds a key with no name", id)
		}

		if variable == "" {
			return citypes.Resource{}, fmt.Errorf(
				"reading the data of secret %s: key %q names no environment variable", id, key)
		}

		data[key] = variable
	}

	return citypes.Resource{
		Kind: kindSecret,
		Name: id,
		Spec: map[string]any{
			"apiServer": apiServer,
			"namespace": namespace,
			"name":      name,
			"data":      data,
		},
	}, nil
}

func entryString(index int, held map[string]any, key string) (string, error) {
	value, err := citypes.SpecString(held, key)
	if err != nil {
		return "", fmt.Errorf("reading spec.resources[%d]: %w", index, err)
	}

	if value == "" {
		return "", fmt.Errorf("reading spec.resources[%d]: %s is required", index, key)
	}

	return value, nil
}

func requiredString(spec map[string]any, key string) (string, error) {
	value, err := citypes.SpecString(spec, key)
	if err != nil {
		return "", err
	}

	if value == "" {
		return "", fmt.Errorf("this engine needs spec.%s and the spec does not name it", key)
	}

	return value, nil
}
