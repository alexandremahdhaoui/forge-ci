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

func SpecBool(spec map[string]any, key string) (bool, error) {
	value, declared := spec[key]
	if !declared || value == nil {
		return false, nil
	}

	flag, isBool := value.(bool)
	if !isBool {
		return false, fmt.Errorf("reading spec.%s: a bool is required, the spec holds a %T", key, value)
	}

	return flag, nil
}

func SpecMap(spec map[string]any, key string) (map[string]any, error) {
	value, declared := spec[key]
	if !declared || value == nil {
		return nil, nil
	}

	held, isMap := value.(map[string]any)
	if !isMap {
		return nil, fmt.Errorf("reading spec.%s: a map is required, the spec holds a %T", key, value)
	}

	return held, nil
}

func SpecStringSlice(spec map[string]any, key string) ([]string, error) {
	value, declared := spec[key]
	if !declared || value == nil {
		return nil, nil
	}

	held, isList := value.([]any)
	if !isList {
		return nil, fmt.Errorf(
			"reading spec.%s: a list of strings is required, the spec holds a %T", key, value)
	}

	out := make([]string, 0, len(held))

	for index, entry := range held {
		text, isString := entry.(string)
		if !isString {
			return nil, fmt.Errorf(
				"reading spec.%s[%d]: a string is required, the spec holds a %T", key, index, entry)
		}

		out = append(out, text)
	}

	return out, nil
}
