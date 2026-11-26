package normalize

import (
	"encoding/base64"
	"unicode"
	"unicode/utf8"
)

// convertSecretDataToStringData moves values of data that decode to
// printable text into stringData, so they can be read and edited. Binary
// values, and text with control characters (other than newline, tab and
// carriage return), stay in data: YAML would show them only as escape
// sequences.
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
		if err != nil || !isPrintableText(decoded) {
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

// isPrintableText reports whether b is valid UTF-8 that contains only
// printable characters (including the space), newlines, tabs and carriage
// returns.
func isPrintableText(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}

	for _, r := range string(b) {
		switch {
		case r == '\n', r == '\t', r == '\r':
		case unicode.IsPrint(r):
		default:
			return false
		}
	}

	return true
}

func redactSecretValues(obj map[string]any) {
	if !isCoreV1Secret(obj) {
		return
	}

	redactSecretField(obj, "data")
	redactSecretField(obj, "stringData")
	redactSecretAnnotations(obj)
}

func isCoreV1Secret(obj map[string]any) bool {
	kind, _ := obj["kind"].(string)
	if kind != kindSecret {
		return false
	}

	apiVersion, _ := obj["apiVersion"].(string)

	return apiVersion == "" || apiVersion == "v1"
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
