package normalize

import (
	"encoding/base64"
	"unicode"
	"unicode/utf8"
)

// convertSecretDataToStringData moves values that decode to printable text
// into stringData, so they can be read and edited. All other values (binary
// data, control characters other than newline, tab and carriage return,
// invisible formatting characters such as a zero-width joiner or a BOM) stay
// base64-encoded in data.
//
// stringData is first folded into data (stringData wins, like in the
// api-server), so the result does not depend on how the input split the
// keys: a dump with stringData and the api-server's data normalize to the
// same document.
func convertSecretDataToStringData(obj map[string]any) {
	if !isCoreV1Secret(obj) {
		return
	}

	oldStringData, _ := obj["stringData"].(map[string]any)
	for _, val := range oldStringData {
		if _, ok := val.(string); !ok {
			return // Not a valid Secret: leave it as it is.
		}
	}

	data, _ := obj["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
	}

	for key, val := range oldStringData {
		text, _ := val.(string) // checked above
		data[key] = base64.StdEncoding.EncodeToString([]byte(text))
	}

	stringData := map[string]any{}

	for key, val := range data {
		encoded, ok := val.(string)
		if !ok {
			continue
		}

		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || !isPrintableText(decoded) {
			continue
		}

		stringData[key] = string(decoded)

		delete(data, key)
	}

	if len(data) == 0 {
		delete(obj, "data")
	} else {
		obj["data"] = data
	}

	if len(stringData) == 0 {
		delete(obj, "stringData")
	} else {
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
