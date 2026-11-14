package normalize

import (
	"encoding/base64"
	"regexp"
	"unicode/utf8"
)

// convertSecretDataToStringData moves values of data that decode to valid
// UTF-8 text into stringData. Binary values stay in data.
func convertSecretDataToStringData(obj map[string]any) {
	if !isCoreV1Secret(obj) {
		return
	}

	data, ok := obj["data"].(map[string]any)
	if !ok {
		return
	}

	stringData, _ := obj["stringData"].(map[string]any)
	if stringData == nil {
		stringData = map[string]any{}
	}

	for key, val := range data {
		encoded, ok := val.(string)
		if !ok {
			continue
		}

		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || !utf8.Valid(decoded) {
			continue
		}

		// The api-server lets stringData win over data.
		if _, ok := stringData[key]; !ok {
			stringData[key] = string(decoded)
		}

		delete(data, key)
	}

	if len(data) == 0 {
		delete(obj, "data")
	}

	if len(stringData) > 0 {
		obj["stringData"] = stringData
	}
}

func redactSecretValues(obj map[string]any) {
	if !isCoreV1Secret(obj) {
		return
	}

	redactSecretField(obj, "data")
	redactSecretField(obj, "stringData")
	redactSecretAnnotations(obj)
}

var secretAPIGroupRegex = regexp.MustCompile(`^v\d+`)

func isCoreV1Secret(obj map[string]any) bool {
	kind, _ := obj["kind"].(string)
	if kind != kindSecret {
		return false
	}

	apiVersion, _ := obj["apiVersion"].(string)
	if apiVersion == "" {
		return true
	}

	if secretAPIGroupRegex.MatchString(apiVersion) {
		return true
	}

	return false
}

func redactSecretField(obj map[string]any, field string) {
	entries, _ := obj[field].(map[string]any)
	for key := range entries {
		entries[key] = RedactionMarker
	}
}

func redactSecretAnnotations(obj map[string]any) {
	metadata, ok := obj[fieldMetadata].(map[string]any)
	if !ok {
		return
	}

	if annotations, ok := metadata["annotations"].(map[string]any); ok {
		delete(annotations, lastAppliedAnnotation)
	}
}
