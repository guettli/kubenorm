package normalize

// This file maps the call style of the tests that were moved from dumpall
// (github.com/guettli/dumpall) onto the normalize API, so they could be
// moved without rewriting them.

import (
	"io"
	"os"
	"path/filepath"
	"regexp"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type options struct {
	outputDir           string
	quiet               bool
	dumpSecrets         bool
	secretsAsStringData bool
	dumpManagedFields   bool
	skipOwned           bool
	ignoreRules         []ignoreRule
	skipRules           []skipRule
	excludeNamespaces   []string
	namespaceFilter     map[string]struct{}
	kindFilter          []string
	nameFilterEnabled   bool
	nameFilterRegex     *regexp.Regexp
}

func (o *options) toOptions() *Options {
	opts := &Options{
		Config: &Config{
			removeRules:       o.ignoreRules,
			skipRules:         o.skipRules,
			excludeNamespaces: o.excludeNamespaces,
		},
		Namespaces:          o.namespaceFilter,
		Kinds:               o.kindFilter,
		SkipOwned:           o.skipOwned,
		KeepManagedFields:   o.dumpManagedFields,
		RedactSecrets:       !o.dumpSecrets,
		SecretsAsStringData: o.secretsAsStringData,
	}

	if o.nameFilterEnabled {
		opts.NameRegex = o.nameFilterRegex
	}

	return opts
}

func writeYAML(filePath string, obj map[string]any, o *options) error {
	opts := o.toOptions()

	err := opts.Normalize(obj)
	if err != nil {
		return err
	}

	f, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	return Encode(f, obj, "test header")
}

func shouldProcessItem(item *unstructured.Unstructured, isNamespaced bool, o *options) bool {
	return o.toOptions().Include(item, isNamespaced)
}

func applyIgnoreRules(obj map[string]any, o *options) {
	o.toOptions().applyRemoveRules(obj)
}

func readYamlFromDir(dir string, o *options) error {
	resources, err := ReadPath(dir, nil)
	if err != nil {
		return err
	}

	resources, err = o.toOptions().Process(resources)
	if err != nil {
		return err
	}

	rendered, err := Render(resources)
	if err != nil {
		return err
	}

	return rendered.WriteDir(filepath.Clean(o.outputDir))
}

func parseLegacy(name string, data []byte) ([]ignoreRule, []skipRule, []string, error) {
	cfg, err := parseIgnoreConfigBytes(name, data)
	if err != nil {
		return nil, nil, nil, err
	}

	return cfg.removeRules, cfg.skipRules, cfg.excludeNamespaces, nil
}

func loadIgnoreRules(useCommon bool, file string) ([]ignoreRule, []skipRule, []string, error) {
	cfg, err := LoadConfig(useCommon, file)
	if err != nil {
		return nil, nil, nil, err
	}

	return cfg.removeRules, cfg.skipRules, cfg.excludeNamespaces, nil
}

func writeCommonConfig(w io.Writer) error {
	_, err := w.Write(CommonConfig())
	return err
}
