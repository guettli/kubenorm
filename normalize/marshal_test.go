package normalize

import (
	"testing"

	"sigs.k8s.io/yaml"
)

func TestMarshalYAMLDeterministic(t *testing.T) {
	t.Parallel()

	// yaml.v2 orders these keys differently depending on Go's map order.
	obj := map[string]any{"0A00": 1, "56": 1, "000170x711": 1, "1000100": 1, "metadata": 1}

	first, err := marshalYAML(obj)
	if err != nil {
		t.Fatal(err)
	}

	for range 200 {
		got, err := marshalYAML(obj)
		if err != nil {
			t.Fatal(err)
		}

		if string(got) != string(first) {
			t.Fatalf("output changed:\n%s\nvs\n%s", first, got)
		}
	}
}

func TestMarshalYAMLLikeKubectl(t *testing.T) {
	t.Parallel()

	obj := map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "labels": map[string]any{"a10": "x", "a2": "y", "B": "z"}},
		"data":       map[string]any{"file-10.txt": "", "file-2.txt": "yes", "n": "1", "f": 1.5, "i": int64(7)},
		"list":       []any{map[string]any{"z": nil, "a": true}, "s"},
	}

	want, err := yaml.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}

	got, err := marshalYAML(obj)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != string(want) {
		t.Errorf("got:\n%s\nwant (sigs.k8s.io/yaml):\n%s", got, want)
	}
}
