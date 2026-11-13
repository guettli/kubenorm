package normalize

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/kustomize/kyaml/kio"
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

// ParseYAML parses a (multi-document) YAML stream. Documents without
// metadata (for example kustomization.yaml or empty documents) are skipped.
func ParseYAML(data []byte, source string) ([]Resource, error) {
	nodes, err := kio.FromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse YAML in %s: %w", source, err)
	}

	resources := make([]Resource, 0, len(nodes))

	for i, node := range nodes {
		m, err := node.Map()
		if err != nil {
			return nil, fmt.Errorf("failed to convert document %d in %s to map: %w", i+1, source, err)
		}

		if _, hasMetadata := m[fieldMetadata]; !hasMetadata {
			continue
		}

		resources = append(resources, Resource{Object: m, Source: source, SOPSEncrypted: isSOPSEncrypted(m)})
	}

	return resources, nil
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

	var resources []Resource

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("failed to read file %s: %w", file, err)
		}

		parsed, err := ParseYAML(data, file)
		if err != nil {
			return nil, err
		}

		resources = append(resources, parsed...)
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
	scope := o.scopeResolver(resources)

	kept := make([]Resource, 0, len(resources))

	for _, r := range resources {
		if r.SOPSEncrypted {
			if o.SkipSOPS {
				continue
			}

			return nil, fmt.Errorf("%s: %w: normalizing it would break the SOPS MAC; exclude the file or use --skip-sops", describe(r), ErrSOPSEncrypted)
		}

		u := r.Unstructured()

		o.applyNamespace(u, scope)

		if !o.Include(u, r.Namespaced()) {
			continue
		}

		err := o.Normalize(r.Object)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", describe(r), err)
		}

		kept = append(kept, r)
	}

	return kept, nil
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
	out := make(Rendered, len(resources))
	sources := make(map[string]string, len(resources))

	for _, r := range resources {
		rel := r.RelPath()
		if prev, ok := sources[rel]; ok {
			return nil, fmt.Errorf("%w %s: found in %s and %s", ErrDuplicate, rel, prev, r.Source)
		}

		sources[rel] = r.Source

		var buf bytes.Buffer

		err := Encode(&buf, r.Object, headerLines...)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", describe(r), err)
		}

		out[rel] = buf.Bytes()
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
