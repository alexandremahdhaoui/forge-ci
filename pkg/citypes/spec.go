package citypes

import "fmt"

func SpecString(spec map[string]any, key string) (string, error) {
	value, declared := spec[key]
	if !declared || value == nil {
		return "", nil
	}

	text, isString := value.(string)
	if !isString {
		return "", fmt.Errorf("reading spec.%s: a string is required, the spec holds a %T", key, value)
	}

	return text, nil
}
