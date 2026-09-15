package clusterresourcecontroller

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	kindHelmRelease = "helm-release"
	kindSecret      = "secret"

	staleSecretEntryKey = "data"
)

var secretEntryKeys = []string{"kind", "namespace", "name", "keys"}

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

	held := in.Spec["resources"]

	declared, ok := held.([]any)
	if held != nil && !ok {
		return citypes.DeclareOutput{}, fmt.Errorf(
			"reading spec.resources: a list is required, the spec holds a %T", held)
	}

	if len(declared) == 0 {
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
	if err := onlySecretEntryKeys(index, held); err != nil {
		return citypes.Resource{}, err
	}

	namespace, err := entryString(index, held, "namespace")
	if err != nil {
		return citypes.Resource{}, err
	}

	name, err := entryString(index, held, "name")
	if err != nil {
		return citypes.Resource{}, err
	}

	id := namespace + "/" + name

	declared, err := citypes.SpecStringSlice(held, "keys")
	if err != nil {
		return citypes.Resource{}, fmt.Errorf("reading the keys of secret %s: %w", id, err)
	}

	if len(declared) == 0 {
		return citypes.Resource{}, fmt.Errorf(
			"reading the keys of secret %s: keys is required, and it names every key the live secret must hold",
			id)
	}

	keys := make([]any, 0, len(declared))

	for _, key := range declared {
		if strings.TrimSpace(key) == "" {
			return citypes.Resource{}, fmt.Errorf(
				"reading the keys of secret %s: keys holds a key with no name", id)
		}

		keys = append(keys, key)
	}

	return citypes.Resource{Kind: kindSecret, Name: id, Spec: map[string]any{
		"apiServer": apiServer,
		"namespace": namespace,
		"name":      name,
		"keys":      keys,
	}}, nil
}

func onlySecretEntryKeys(index int, held map[string]any) error {
	var unknown []string

	for key := range held {
		if !slices.Contains(secretEntryKeys, key) {
			unknown = append(unknown, key)
		}
	}

	if len(unknown) == 0 {
		return nil
	}

	slices.Sort(unknown)

	note := ""
	if slices.Contains(unknown, staleSecretEntryKey) {
		note = ". " + staleSecretEntryKey + " is no longer read, and keys names every key " +
			"the live secret must hold, never a value"
	}

	return fmt.Errorf(
		"reading spec.resources[%d]: a secret entry names %s, and a secret entry holds %s%s",
		index, strings.Join(unknown, " and "), strings.Join(secretEntryKeys, ", "), note)
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
