package clusterresourcecontroller

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const kindReleaseDocument = "HelmRelease"

type documentReference struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type documentMetadata struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type releaseDocument struct {
	Kind     string           `json:"kind"`
	Metadata documentMetadata `json:"metadata"`
	Spec     struct {
		Chart struct {
			Spec struct {
				Chart     string            `json:"chart"`
				Version   string            `json:"version"`
				SourceRef documentReference `json:"sourceRef"`
			} `json:"spec"`
		} `json:"chart"`
	} `json:"spec"`
}

type repositoryDocument struct {
	Metadata documentMetadata `json:"metadata"`
	Spec     struct {
		URL string `json:"url"`
	} `json:"spec"`
}

func (c *Controller) helmRelease(
	root string, index int, held map[string]any, apiServer string,
) (citypes.Resource, error) {
	releaseFile, err := entryString(index, held, "helmReleaseFile")
	if err != nil {
		return citypes.Resource{}, err
	}

	valuesFile, err := entryString(index, held, "valuesFile")
	if err != nil {
		return citypes.Resource{}, err
	}

	createNamespace, err := citypes.SpecBool(held, "createNamespace")
	if err != nil {
		return citypes.Resource{}, fmt.Errorf("reading spec.resources[%d]: %w", index, err)
	}

	document, err := c.readRelease(root, releaseFile)
	if err != nil {
		return citypes.Resource{}, err
	}

	id := document.Metadata.Namespace + "/" + document.Metadata.Name

	repository, err := c.resolveRepository(root, releaseFile, document, id)
	if err != nil {
		return citypes.Resource{}, err
	}

	values, err := c.readValues(root, valuesFile, id)
	if err != nil {
		return citypes.Resource{}, err
	}

	return citypes.Resource{
		Kind: kindHelmRelease,
		Name: id,
		Spec: map[string]any{
			"apiServer":       apiServer,
			"namespace":       document.Metadata.Namespace,
			"name":            document.Metadata.Name,
			"chart":           document.Spec.Chart.Spec.Chart,
			"version":         document.Spec.Chart.Spec.Version,
			"repository":      repository,
			"values":          values,
			"createNamespace": createNamespace,
		},
	}, nil
}

func (c *Controller) readRelease(root, releaseFile string) (releaseDocument, error) {
	raw, err := c.fs.ReadFile(filepath.Join(root, filepath.FromSlash(releaseFile)))
	if err != nil {
		return releaseDocument{}, fmt.Errorf("reading the release document %s: %w", releaseFile, err)
	}

	var document releaseDocument

	if err := yaml.Unmarshal(raw, &document); err != nil {
		return releaseDocument{}, fmt.Errorf("reading the release document %s: %w", releaseFile, err)
	}

	if document.Kind != kindReleaseDocument {
		return releaseDocument{}, fmt.Errorf(
			"reading the release document %s: its kind is %q and %s is required",
			releaseFile, document.Kind, kindReleaseDocument)
	}

	chart := document.Spec.Chart.Spec

	required := []struct {
		key   string
		value string
	}{
		{"metadata.namespace", document.Metadata.Namespace},
		{"metadata.name", document.Metadata.Name},
		{"spec.chart.spec.chart", chart.Chart},
		{"spec.chart.spec.version", chart.Version},
		{"spec.chart.spec.sourceRef.kind", chart.SourceRef.Kind},
		{"spec.chart.spec.sourceRef.name", chart.SourceRef.Name},
		{"spec.chart.spec.sourceRef.namespace", chart.SourceRef.Namespace},
	}

	for _, field := range required {
		if field.value == "" {
			return releaseDocument{}, fmt.Errorf(
				"reading the release document %s: %s is required and the document does not name it",
				releaseFile, field.key)
		}
	}

	return document, nil
}

func (c *Controller) resolveRepository(
	root, releaseFile string, document releaseDocument, id string,
) (string, error) {
	ref := document.Spec.Chart.Spec.SourceRef
	dir := filepath.Join(root, filepath.FromSlash(path.Dir(releaseFile)))

	names, err := c.fs.List(dir)
	if err != nil {
		return "", fmt.Errorf("resolving the chart repository of release %s: %w", id, err)
	}

	matched, url, malformed := "", "", ""

	for _, name := range names {
		if !namesADocument(name) {
			continue
		}

		raw, err := c.fs.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "", fmt.Errorf(
				"resolving the chart repository of release %s: its sourceRef names %s %s/%s "+
					"and %s sits beside %s and cannot be read: %w",
				id, ref.Kind, ref.Namespace, ref.Name, name, releaseFile, err)
		}

		if !namesKind(raw, ref.Kind) {
			continue
		}

		var candidate repositoryDocument

		if err := yaml.Unmarshal(raw, &candidate); err != nil {
			if malformed == "" {
				malformed = name
			}

			continue
		}

		if candidate.Metadata.Name != ref.Name ||
			candidate.Metadata.Namespace != ref.Namespace {
			continue
		}

		if matched == "" {
			matched, url = name, candidate.Spec.URL
		}
	}

	if malformed != "" {
		return "", fmt.Errorf(
			"resolving the chart repository of release %s: its sourceRef names %s %s/%s "+
				"and %s names that kind and does not parse as one",
			id, ref.Kind, ref.Namespace, ref.Name, malformed)
	}

	if matched == "" {
		return "", fmt.Errorf(
			"resolving the chart repository of release %s: its sourceRef names %s %s/%s "+
				"and no document beside %s is one",
			id, ref.Kind, ref.Namespace, ref.Name, releaseFile)
	}

	if url == "" {
		return "", fmt.Errorf(
			"resolving the chart repository of release %s: %s names no spec.url", id, matched)
	}

	return url, nil
}

func (c *Controller) readValues(root, valuesFile, id string) (map[string]any, error) {
	raw, err := c.fs.ReadFile(filepath.Join(root, filepath.FromSlash(valuesFile)))
	if err != nil {
		return nil, fmt.Errorf("reading the values of release %s from %s: %w", id, valuesFile, err)
	}

	values := map[string]any{}

	if err := yaml.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf(
			"reading the values of release %s from %s: the file holds no yaml mapping", id, valuesFile)
	}

	return values, nil
}

func namesKind(raw []byte, kind string) bool {
	var top map[string]any

	if err := yaml.Unmarshal(raw, &top); err != nil {
		return false
	}

	named, _ := top["kind"].(string)

	return named == kind
}

func namesADocument(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}
