package normalize

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// StdinPath is the path argument that means "read from stdin".
const StdinPath = "-"

// Resource is a single Kubernetes object read from a YAML document.
type Resource struct {
	// Object is the decoded YAML document.
	Object map[string]any
	// Source is the file the document was read from ("-" for stdin).
	Source string
	// SOPSEncrypted is true if the document carries SOPS metadata (a
	// top-level "sops" map with a "mac"). Re-encoding such a document
	// reorders keys and breaks the MAC, so Process refuses or skips it.
	SOPSEncrypted bool
}

// ErrSOPSEncrypted is returned by Process for SOPS-encrypted documents
// unless Options.SkipSOPS is set.
var ErrSOPSEncrypted = errors.New("document is SOPS-encrypted")

func isSOPSEncrypted(obj map[string]any) bool {
	sops, ok := obj["sops"].(map[string]any)
	if !ok {
		return false
	}

	_, hasMAC := sops["mac"]

	return hasMAC
}

// Unstructured wraps the object without copying it.
func (r *Resource) Unstructured() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: r.Object}
}

// Namespaced reports whether metadata.namespace is set. Without an api-server
// this is the only reliable signal: resources without namespace are treated
// as cluster-scoped for the output layout.
func (r *Resource) Namespaced() bool {
	return r.Unstructured().GetNamespace() != ""
}

// RelPath returns the stable relative output path, see RelPath.
func (r *Resource) RelPath() string {
	return RelPath(r.Unstructured(), r.Namespaced())
}

// ParseYAML parses a (multi-document) YAML stream the way kubectl does
// (sigs.k8s.io/yaml, YAML 1.1 semantics, duplicate keys are an error).
// Documents without metadata (for example kustomization.yaml) and empty
// documents are skipped.
func ParseYAML(data []byte, source string) ([]Resource, error) {
	docs, err := splitDocuments(data, source)
	if err != nil {
		return nil, err
	}

	resources := make([]Resource, 0, len(docs))

	for _, m := range docs {
		if _, hasMetadata := m[fieldMetadata]; !hasMetadata {
			continue
		}

		resources = append(resources, Resource{Object: m, Source: source, SOPSEncrypted: isSOPSEncrypted(m)})
	}

	return resources, nil
}

// CountDocuments returns the number of non-empty YAML documents in data.
func CountDocuments(data []byte) (int, error) {
	docs, err := splitDocuments(data, "input")
	if err != nil {
		return 0, err
	}

	return len(docs), nil
}

// splitDocuments decodes every non-empty document into a map.
func splitDocuments(data []byte, source string) ([]map[string]any, error) {
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))

	var docs []map[string]any

	for i := 1; ; i++ {
		doc, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return docs, nil
		}

		if err != nil {
			return nil, fmt.Errorf("failed to read document %d in %s: %w", i, source, err)
		}

		jsonDoc, err := yaml.YAMLToJSONStrict(doc)
		if err != nil {
			return nil, fmt.Errorf("failed to parse document %d in %s: %w", i, source, err)
		}

		if bytes.Equal(bytes.TrimSpace(jsonDoc), []byte("null")) {
			continue
		}

		var m map[string]any

		err = utiljson.Unmarshal(jsonDoc, &m)
		if err != nil {
			return nil, fmt.Errorf("document %d in %s is not a YAML map: %w", i, source, err)
		}

		docs = append(docs, m)
	}
}

// ReadPath reads all resources from a file, a directory (recursively, *.yaml
// and *.yml) or stdin (path "-").
func ReadPath(inputPath string, stdin io.Reader) ([]Resource, error) {
	if inputPath == StdinPath {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("failed to read stdin: %w", err)
		}

		return ParseYAML(data, StdinPath)
	}

	info, err := os.Stat(inputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect %q: %w", inputPath, err)
	}

	files := []string{inputPath}

	if info.IsDir() {
		files, err = FindYAMLFiles(inputPath)
		if err != nil {
			return nil, err
		}
	}

	// Parsing dominates the runtime, so files are parsed in parallel.
	perFile := make([][]Resource, len(files))

	err = parallelFor(len(files), func(i int) error {
		data, err := os.ReadFile(files[i])
		if err != nil {
			return fmt.Errorf("failed to read file %s: %w", files[i], err)
		}

		perFile[i], err = ParseYAML(data, files[i])

		return err
	})
	if err != nil {
		return nil, err
	}

	var resources []Resource
	for _, parsed := range perFile {
		resources = append(resources, parsed...)
	}

	return resources, nil
}

// ReadPaths reads all resources from several inputs, see ReadPath. stdin
// ("-") must be the only input.
func ReadPaths(inputPaths []string, stdin io.Reader) ([]Resource, error) {
	if len(inputPaths) > 1 && slices.Contains(inputPaths, StdinPath) {
		return nil, errors.New("stdin (-) cannot be combined with other inputs")
	}

	var resources []Resource

	for _, p := range inputPaths {
		r, err := ReadPath(p, stdin)
		if err != nil {
			return nil, err
		}

		resources = append(resources, r...)
	}

	return resources, nil
}

// FindYAMLFiles returns all *.yaml / *.yml files below dirPath, sorted.
func FindYAMLFiles(dirPath string) ([]string, error) {
	info, err := os.Stat(dirPath)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect directory %s: %w", dirPath, err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("input path %q is not a directory", dirPath)
	}

	var yamlFiles []string

	err = filepath.WalkDir(dirPath, func(currentPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if !d.IsDir() && IsYAMLFile(currentPath) {
			yamlFiles = append(yamlFiles, currentPath)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk directory %s: %w", dirPath, err)
	}

	sort.Strings(yamlFiles)

	return yamlFiles, nil
}

// IsYAMLFile reports whether the file name ends with .yaml or .yml.
func IsYAMLFile(filePath string) bool {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".yaml", ".yml":
		return true
	default:
		return false
	}
}

// Process applies namespace handling, filters and normalization. Resources
// that are filtered out are dropped. The objects are modified in place.
func (o *Options) Process(resources []Resource) ([]Resource, error) {
	kept, _, err := o.process(resources, false)

	return kept, err
}

// process is Process. With explain set it also returns what happened to
// every resource.
func (o *Options) process(resources []Resource, explain bool) ([]Resource, []Explanation, error) {
	scope := o.scopeResolver(resources)

	type candidate struct {
		resource Resource
		tracer   *tracer
		index    int
	}

	var explanations []Explanation

	candidates := make([]candidate, 0, len(resources))
	moves := namespaceMoves{}

	for _, r := range resources {
		if r.SOPSEncrypted {
			if !o.SkipSOPS {
				return nil, nil, fmt.Errorf("%s: %w: normalizing it would break the SOPS MAC; exclude the file or use --skip-sops", describe(r), ErrSOPSEncrypted)
			}

			if explain {
				explanations = append(explanations, Explanation{Source: r.Source, Path: r.RelPath(), Dropped: "SOPS-encrypted (--skip-sops)"})
			}

			continue
		}

		c := candidate{resource: r, index: -1}
		if explain {
			c.tracer = &tracer{}
			c.index = len(explanations)
			explanations = append(explanations, Explanation{Source: r.Source})
		}

		o.applyNamespace(r.Unstructured(), scope, moves, c.tracer)

		candidates = append(candidates, c)
	}

	if !o.KeepNamespaceReferences && len(moves) > 0 {
		for _, c := range candidates {
			c.tracer.step(c.resource.Object, "reference to an object in a new namespace", func() {
				moves.updateReferences(c.resource.Object)
			})
		}
	}

	include := make([]bool, len(candidates))

	// Each resource is normalized independently, so this runs in parallel.
	// Every goroutine only writes its own index.
	err := parallelFor(len(candidates), func(i int) error {
		c := candidates[i]
		r := c.resource

		if explain {
			explanations[c.index].Path = r.RelPath()
		}

		if reason := o.SkipReason(r.Unstructured(), r.Namespaced()); reason != "" {
			if explain {
				explanations[c.index].Dropped = reason
			}

			return nil
		}

		err := o.normalize(r.Object, c.tracer)
		if err != nil {
			return fmt.Errorf("%s: %w", describe(r), err)
		}

		if explain {
			explanations[c.index].Changes = c.tracer.changes
		}

		include[i] = true

		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	kept := make([]Resource, 0, len(candidates))

	for i, c := range candidates {
		if include[i] {
			kept = append(kept, c.resource)
		}
	}

	return kept, explanations, nil
}

func describe(r Resource) string {
	u := r.Unstructured()

	return fmt.Sprintf("%s %s/%s from %s", u.GetKind(), u.GetNamespace(), u.GetName(), r.Source)
}

// ErrDuplicate is returned by Render when two resources map to the same path.
var ErrDuplicate = errors.New("duplicate resource")

// Rendered maps the relative output path (see RelPath) to the encoded YAML.
type Rendered map[string][]byte

// Render encodes each resource. Two resources with the same identity are an
// error, because one would silently overwrite the other.
func Render(resources []Resource, headerLines ...string) (Rendered, error) {
	sources := make(map[string]string, len(resources))
	paths := make([]string, len(resources))

	for i, r := range resources {
		rel := r.RelPath()
		if prev, ok := sources[rel]; ok {
			return nil, fmt.Errorf("%w %s: found in %s and %s", ErrDuplicate, rel, prev, r.Source)
		}

		sources[rel] = r.Source
		paths[i] = rel
	}

	encoded := make([][]byte, len(resources))

	err := parallelFor(len(resources), func(i int) error {
		var buf bytes.Buffer

		err := Encode(&buf, resources[i].Object, headerLines...)
		if err != nil {
			return fmt.Errorf("%s: %w", describe(resources[i]), err)
		}

		encoded[i] = buf.Bytes()

		return nil
	})
	if err != nil {
		return nil, err
	}

	out := make(Rendered, len(resources))
	for i, rel := range paths {
		out[rel] = encoded[i]
	}

	return out, nil
}

// SortedPaths returns the keys sorted.
func (r Rendered) SortedPaths() []string {
	paths := make([]string, 0, len(r))
	for p := range r {
		paths = append(paths, p)
	}

	sort.Strings(paths)

	return paths
}

// WriteDir writes every file below dir, creating directories as needed.
func (r Rendered) WriteDir(dir string) error {
	for _, rel := range r.SortedPaths() {
		target := filepath.Join(dir, rel)

		err := os.MkdirAll(filepath.Dir(target), 0o755)
		if err != nil {
			return fmt.Errorf("failed to create directory: %w", err)
		}

		err = os.WriteFile(target, r[rel], 0o644) //nolint:gosec // manifests are not secret by default
		if err != nil {
			return fmt.Errorf("failed to write %s: %w", target, err)
		}
	}

	return nil
}

// WriteStream writes all documents as one multi-document YAML stream, sorted
// by path, separated by "---".
func (r Rendered) WriteStream(w io.Writer) error {
	for i, rel := range r.SortedPaths() {
		if i > 0 {
			_, err := io.WriteString(w, "---\n")
			if err != nil {
				return fmt.Errorf("writing separator: %w", err)
			}
		}

		_, err := w.Write(r[rel])
		if err != nil {
			return fmt.Errorf("writing %s: %w", rel, err)
		}
	}

	return nil
}
