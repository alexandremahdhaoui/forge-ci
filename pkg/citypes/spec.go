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

func SpecStringMap(spec map[string]any, key string) (map[string]string, error) {
	value, declared := spec[key]
	if !declared || value == nil {
		return nil, nil
	}

	held, isMap := value.(map[string]any)
	if !isMap {
		return nil, fmt.Errorf(
			"reading spec.%s: a map of strings is required, the spec holds a %T", key, value)
	}

	out := make(map[string]string, len(held))

	for name, entry := range held {
		text, isString := entry.(string)
		if !isString {
			return nil, fmt.Errorf(
				"reading spec.%s.%s: a string is required, the spec holds a %T", key, name, entry)
		}

		out[name] = text
	}

	return out, nil
}
