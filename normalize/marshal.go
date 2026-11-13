package normalize

import (
	"encoding/json"
	"fmt"
	"sort"
	"unicode"

	yamlv2 "go.yaml.in/yaml/v2"
)

// marshalYAML is sigs.k8s.io/yaml.Marshal (the kubectl printer) with a
// deterministic key order.
//
// sigs.k8s.io/yaml converts JSON to YAML with yaml.v2, which sorts map keys
// with a "natural" order that is not transitive for keys like "0A00", "56"
// and "000170x711". sort.Sort then depends on Go's random map order and the
// output changes from run to run. Here the keys are sorted stably with the
// same comparison, starting from the byte order of encoding/json. Where
// yaml.v2 is deterministic, the result is identical.
func marshalYAML(obj any) ([]byte, error) {
	data, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("marshaling JSON: %w", err)
	}

	var doc yamlv2.MapSlice

	err = yamlv2.Unmarshal(data, &doc)
	if err != nil {
		return nil, fmt.Errorf("converting JSON to YAML: %w", err)
	}

	sortMapSlices(doc)

	out, err := yamlv2.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshaling YAML: %w", err)
	}

	return out, nil
}

func sortMapSlices(v any) {
	switch typed := v.(type) {
	case yamlv2.MapSlice:
		sort.SliceStable(typed, func(i, j int) bool {
			a, _ := typed[i].Key.(string) // JSON keys are strings
			b, _ := typed[j].Key.(string)

			return yamlKeyLess([]rune(a), []rune(b))
		})

		for _, item := range typed {
			sortMapSlices(item.Value)
		}
	case []any:
		for _, item := range typed {
			sortMapSlices(item)
		}
	}
}

// yamlKeyLess is the string comparison of yaml.v2 (keyList.Less in
// sorter.go, Apache License 2.0): runs of digits compare as numbers,
// letters sort before other characters.
func yamlKeyLess(ar, br []rune) bool {
	for i := 0; i < len(ar) && i < len(br); i++ {
		if ar[i] == br[i] {
			continue
		}

		al := unicode.IsLetter(ar[i])
		bl := unicode.IsLetter(br[i])

		if al && bl {
			return ar[i] < br[i]
		}

		if al || bl {
			return bl
		}

		var an, bn int64

		if ar[i] == '0' || br[i] == '0' {
			for j := i - 1; j >= 0 && unicode.IsDigit(ar[j]); j-- {
				if ar[j] != '0' {
					an = 1
					bn = 1

					break
				}
			}
		}

		ai := i
		for ; ai < len(ar) && unicode.IsDigit(ar[ai]); ai++ {
			an = an*10 + int64(ar[ai]-'0')
		}

		bi := i
		for ; bi < len(br) && unicode.IsDigit(br[bi]); bi++ {
			bn = bn*10 + int64(br[bi]-'0')
		}

		if an != bn {
			return an < bn
		}

		if ai != bi {
			return ai < bi
		}

		return ar[i] < br[i]
	}

	return len(ar) < len(br)
}
