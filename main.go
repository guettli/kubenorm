// Command kubenorm normalizes Kubernetes YAML manifests on disk, so that two
// representations of the same desired state become byte-identical. It never
// connects to a cluster.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"slices"
	"strings"

	"github.com/gavv/cobradoc"
	"github.com/guettli/kubenorm/normalize"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	toolName = "kubenorm"

	exitDifferences = 1
	exitError       = 2
)

// errDifferences signals exit code 1 without printing an error message.
var errDifferences = errors.New("differences found")

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	root := buildRootCmd(stdin, stdout, stderr)
	root.SetArgs(args)

	err := root.Execute()

	switch {
	case err == nil:
		return 0
	case errors.Is(err, errDifferences):
		return exitDifferences
	default:
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return exitError
	}
}

func buildRootCmd(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:   toolName,
		Short: "Normalize Kubernetes YAML manifests for clean diffs",
		Long: `kubenorm normalizes Kubernetes YAML manifests on disk.

It removes server-managed and defaulted fields (status, managedFields,
creationTimestamp, default values, tool labels, ...), drops noise resources
and writes every resource to a stable path:

  NAMESPACE/[GROUP_]KIND/NAME.yaml   (cluster-scoped: _cluster/...)

This makes rendered manifests (Rendered Manifests Pattern), helm output and
cluster dumps comparable. kubenorm never connects to a cluster; use dumpall
(https://github.com/guettli/dumpall) to dump live resources.

Exit codes: 0 ok, 1 differences found (diff, norm --check), 2 error.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)

	root.AddCommand(buildNormCmd(), buildDiffCmd(), buildExplainCmd(), buildShowCommonConfigCmd(), buildVersionCmd(), buildGendocsCmd())

	return root
}

// normFlags are shared by norm and diff.
type normFlags struct {
	configFiles           []string
	noCommonConfig        bool
	namespacesCSV         string
	kindsCSV              string
	nameRegex             string
	skipNameGlob          string
	excludeNamespacesCSV  string
	skipOwned             bool
	keepManagedFields     bool
	redactSecrets         bool
	secretsAsStringData   bool
	addNamespaceIfMissing string
	overwriteNamespace    string
	noConfigDiscovery     bool
	crdPaths              []string
	setLabels             []string
	setAnnotations        []string
	renderedBy            string
	// flagSet tells which flags were given.
	flagSet *pflag.FlagSet
}

func (f *normFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	f.flagSet = fl
	fl.StringArrayVarP(&f.configFiles, "config", "c", nil, "YAML config with removeFields/skipResources/excludeNamespaces/quantityFields/clusterScopedResources/setMetadata/ownerHints/keepFields rules (repeatable; merged with the common config)")
	fl.BoolVar(&f.noCommonConfig, "no-common-config", false, "Do not apply the embedded common config (see show-common-config)")
	fl.StringVarP(&f.namespacesCSV, "namespaces", "n", "", "Only keep resources in these namespaces (comma-separated)")
	fl.StringVar(&f.kindsCSV, "kind", "", "Only keep kinds matching these globs (comma-separated, e.g. 'ConfigMap,Cluster*')")
	fl.StringVarP(&f.nameRegex, "name-regex", "x", "", "Only keep resources whose metadata.name matches this regex")
	fl.StringVar(&f.skipNameGlob, "skip-name-glob", "", "Drop resources whose metadata.name matches this anchored glob")
	fl.StringVar(&f.excludeNamespacesCSV, "exclude-namespaces", "", "Drop these namespaces (globs, comma-separated): the Namespace object and everything inside")
	fl.BoolVarP(&f.skipOwned, "skip-owned", "O", false, "Drop resources with a controlling ownerReference and objects Kubernetes autogenerates")
	fl.BoolVar(&f.keepManagedFields, "keep-managed-fields", false, "Keep metadata.managedFields")
	fl.BoolVar(&f.redactSecrets, "redact-secrets", false, "Replace values of Secrets with "+normalize.RedactionMarker)
	fl.BoolVar(&f.secretsAsStringData, "secrets-as-stringdata", false, "Convert base64 Secret data to stringData")
	fl.StringVar(&f.addNamespaceIfMissing, "add-namespace-if-missing", "", "Set metadata.namespace on namespaced resources without namespace, and fill empty namespace references (cluster-scoped kinds are left alone)")
	fl.StringVar(&f.overwriteNamespace, "overwrite-namespace", "", "Set metadata.namespace on all namespaced resources, replacing existing values, and move references to the replaced namespaces")
	fl.StringArrayVar(&f.crdPaths, "crds", nil, "Also read CustomResourceDefinitions from this file or directory (repeatable; other resources there are ignored): their quantity fields, schema defaults and scope are used like CRDs in the input")
	fl.StringArrayVar(&f.setLabels, "set-label", nil, "Set this label (key=value, repeatable) on every resource, replacing an existing value")
	fl.StringArrayVar(&f.setAnnotations, "set-annotation", nil, "Set this annotation (key=value, repeatable) on every resource, replacing an existing value")
	fl.StringVar(&f.renderedBy, "rendered-by", "", "Set the annotation "+normalize.RenderedByAnnotation+" to this value (e.g. the script that renders the manifests) on every resource")
	fl.BoolVar(&f.noConfigDiscovery, "no-config-discovery", false, "Do not load "+discoveredConfigName+" from the current directory or its parents")
}

// cliConfig returns a config with only the rules given as flags.
func (f *normFlags) cliConfig() (*normalize.Config, error) {
	cfg := &normalize.Config{}

	return cfg, f.addCLIRules(cfg)
}

// addCLIRules adds the rules of --skip-name-glob and --exclude-namespaces to
// cfg.
func (f *normFlags) addCLIRules(cfg *normalize.Config) error {
	err := cfg.AddSkipNameGlob(f.skipNameGlob)
	if err != nil {
		return err
	}

	return cfg.AddExcludeNamespacesCSV(f.excludeNamespacesCSV)
}

func (f *normFlags) options(stderr io.Writer) (*normalize.Options, error) {
	configFiles := f.configFiles

	if !f.noConfigDiscovery {
		discovered, err := discoverConfig()
		if err != nil {
			return nil, err
		}

		if discovered != "" {
			configFiles = append([]string{discovered}, configFiles...)
		}
	}

	cfg, err := normalize.LoadConfig(!f.noCommonConfig, configFiles...)
	if err != nil {
		return nil, err
	}

	err = f.addCLIRules(cfg)
	if err != nil {
		return nil, err
	}

	opts, err := f.scopeOptions(cfg)
	if err != nil {
		return nil, err
	}

	err = f.setChangeOptions(opts)
	if err != nil {
		return nil, err
	}

	// diff processes both sides with these options: print each warning once.
	warned := map[string]bool{}
	opts.Warn = func(msg string) {
		if !warned[msg] {
			warned[msg] = true
			fmt.Fprintln(stderr, "Warning: "+msg)
		}
	}

	if slices.Contains(f.crdPaths, normalize.StdinPath) {
		return nil, errors.New("--crds: stdin is not supported")
	}

	if len(f.crdPaths) > 0 {
		crds, err := normalize.ReadPaths(f.crdPaths, nil)
		if err != nil {
			return nil, fmt.Errorf("--crds: %w", err)
		}

		opts = opts.WithCRDs(crds)
	}

	return opts, nil
}

// setChangeOptions sets the options that change resources, not which ones
// are kept.
func (f *normFlags) setChangeOptions(opts *normalize.Options) error {
	opts.KeepManagedFields = f.keepManagedFields
	opts.RedactSecrets = f.redactSecrets
	opts.SecretsAsStringData = f.secretsAsStringData
	opts.AddNamespaceIfMissing = strings.TrimSpace(f.addNamespaceIfMissing)
	opts.OverwriteNamespace = strings.TrimSpace(f.overwriteNamespace)

	var err error

	opts.SetLabels, err = parseKeyValues("--set-label", f.setLabels)
	if err != nil {
		return err
	}

	opts.SetAnnotations, err = parseKeyValues("--set-annotation", f.setAnnotations)
	if err != nil {
		return err
	}

	opts.RenderedBy = strings.TrimSpace(f.renderedBy)
	if opts.RenderedBy == "" && f.flagSet != nil && f.flagSet.Changed("rendered-by") {
		return errors.New("--rendered-by: empty value")
	}

	return nil
}

// parseKeyValues parses "key=value" flag values. The value may contain "=".
func parseKeyValues(flag string, pairs []string) (map[string]string, error) {
	out := make(map[string]string, len(pairs))

	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("%s %q: want key=value", flag, p)
		}

		out[k] = v
	}

	return out, nil
}

// scopeOptions returns options with only the filters set.
func (f *normFlags) scopeOptions(cfg *normalize.Config) (*normalize.Options, error) {
	namespaces, err := normalize.ParseNamespacesCSV(f.namespacesCSV)
	if err != nil {
		return nil, fmt.Errorf("--namespaces: %w", err)
	}

	kinds, err := normalize.ParseKindsCSV(f.kindsCSV)
	if err != nil {
		return nil, fmt.Errorf("--kind: %w", err)
	}

	nameRegex, err := normalize.ParseNameRegex(f.nameRegex)
	if err != nil {
		return nil, fmt.Errorf("--name-regex: %w", err)
	}

	return &normalize.Options{
		Config:     cfg,
		Namespaces: namespaces,
		Kinds:      kinds,
		NameRegex:  nameRegex,
		SkipOwned:  f.skipOwned,
	}, nil
}

func buildNormCmd() *cobra.Command {
	var (
		flags       normFlags
		outDir      string
		cleanOutDir bool
		check       bool
		comment     string
		verbose     bool
	)

	cmd := &cobra.Command{
		Use:   "norm <file|dir|->...",
		Short: "Normalize YAML manifests",
		Long: `Normalize YAML manifests from files, directories (recursive) or stdin ("-").

Without --out-dir the result is written to stdout as one multi-document YAML
stream, sorted by NAMESPACE/KIND/NAME. With --out-dir every resource is
written to its own file NAMESPACE/[GROUP_]KIND/NAME.yaml.

With --check nothing is written: kubenorm shows what normalization would
change and exits 1 if anything would change. Comments, key order and the file
layout are ignored by --check.

Examples:
  helm template my-chart | kubenorm norm - -o zz_rendered/
  kubenorm norm ./k8s/plain --check
  kubenorm norm liqo.yaml --add-namespace-if-missing liqo -o out/`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if check && outDir != "" {
				return errors.New("--check and --out-dir are mutually exclusive")
			}

			if check {
				return runCheck(cmd, &flags, args)
			}

			if cmd.Flags().Changed("remove-out-dir") {
				fmt.Fprintln(cmd.ErrOrStderr(), "Warning: --remove-out-dir (-r) is deprecated and will be removed, use --clean-out-dir")
			}

			return runNorm(cmd, &flags, args, outDir, cleanOutDir, comment, verbose)
		},
	}

	flags.register(cmd)
	cmd.Flags().StringVarP(&outDir, "out-dir", "o", "", "Write one file per resource into this directory (must not exist, see --clean-out-dir)")
	cmd.Flags().BoolVar(&cleanOutDir, "clean-out-dir", false, "Remove --out-dir before writing (only if it contains nothing but *.yaml files)")
	cmd.Flags().BoolVarP(&cleanOutDir, "remove-out-dir", "r", false, "")
	_ = cmd.Flags().MarkHidden("remove-out-dir")
	cmd.Flags().BoolVar(&check, "check", false, "Do not write; exit 1 and show a diff if the input is not normalized")
	cmd.Flags().StringVar(&comment, "comment", "", "Additional comment line at the top of each output file")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Print every written file to stderr")

	return cmd
}

func header(comment string) []string {
	lines := []string{fmt.Sprintf("https://github.com/guettli/kubenorm version: %s", getBuildVersion())}
	if comment != "" {
		lines = append(lines, comment)
	}

	return lines
}

// readInputs reads all resources from the inputs (files, directories or
// "-" for stdin) and notes skipped SOPS-encrypted documents on stderr.
func readInputs(cmd *cobra.Command, inputs ...string) ([]normalize.Resource, error) {
	resources, err := normalize.ReadPaths(inputs, cmd.InOrStdin())
	if err != nil {
		return nil, err
	}

	for _, r := range resources {
		if r.SOPSEncrypted {
			fmt.Fprintf(cmd.ErrOrStderr(), "Skipped SOPS-encrypted %s (normalizing would break its MAC)\n", r.RelPath())
		}
	}

	return resources, nil
}

func runNorm(cmd *cobra.Command, flags *normFlags, inputs []string, outDir string, cleanOutDir bool, comment string, verbose bool) error {
	opts, err := flags.options(cmd.ErrOrStderr())
	if err != nil {
		return err
	}

	resources, err := readInputs(cmd, inputs...)
	if err != nil {
		return err
	}

	resources, err = opts.Process(resources)
	if err != nil {
		return err
	}

	if outDir == "" {
		rendered, err := normalize.Render(resources)
		if err != nil {
			return err
		}

		out := cmd.OutOrStdout()
		for _, line := range header(comment) {
			fmt.Fprintf(out, "# %s\n", line)
		}

		return rendered.WriteStream(out)
	}

	rendered, err := normalize.Render(resources, header(comment)...)
	if err != nil {
		return err
	}

	err = prepareOutDir(outDir, cleanOutDir)
	if err != nil {
		return err
	}

	err = rendered.WriteDir(outDir)
	if err != nil {
		return err
	}

	if verbose {
		for _, rel := range slices.Sorted(maps.Keys(rendered)) {
			fmt.Fprintf(cmd.ErrOrStderr(), "Written: %s/%s\n", outDir, rel)
		}
	}

	return nil
}

func prepareOutDir(outDir string, remove bool) error {
	if remove {
		err := checkRemovable(outDir)
		if err != nil {
			return err
		}

		err = os.RemoveAll(outDir)
		if err != nil {
			return fmt.Errorf("failed to remove out-dir %s: %w", outDir, err)
		}
	}

	_, err := os.Stat(outDir)
	if err == nil {
		return fmt.Errorf("output directory %q already exists, use --clean-out-dir to replace it", outDir)
	}

	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to inspect output directory %q: %w", outDir, err)
	}

	return nil
}

// checkPairs pairs every resource with its normalized version. Process
// changed the originals in place and returned the kept ones; clones are the
// copies taken before. A resource that normalization drops (for example by a
// skipResources rule) stays as raw side only, so the check fails, unless the
// scope filters of rawOpts exclude it too.
func checkPairs(originals, clones, kept []normalize.Resource, rawOpts *normalize.Options) []checkPair {
	keptObjects := make(map[uintptr]bool, len(kept))
	for _, r := range kept {
		keptObjects[reflect.ValueOf(r.Object).Pointer()] = true
	}

	pairs := make([]checkPair, 0, len(originals))

	for i := range originals {
		r := &originals[i]
		if r.SOPSEncrypted {
			continue
		}

		p := checkPair{raw: clones[i], path: r.RelPath()}

		switch {
		case keptObjects[reflect.ValueOf(r.Object).Pointer()]:
			p.normalized = r
		case !rawOpts.Include(r.Unstructured(), r.Namespaced()):
			continue
		}

		pairs = append(pairs, p)
	}

	return pairs
}

// writeSourceDiffs writes the diffs and returns the labels of the sources
// that differ.
func writeSourceDiffs(w io.Writer, sourceDiffs []sourceDiff) ([]string, error) {
	var (
		differ []string
		out    strings.Builder
	)

	for _, sd := range sourceDiffs {
		if len(sd.Diffs) == 0 {
			continue
		}

		differ = append(differ, diffLabel(sd.Source))

		for _, d := range sd.Diffs {
			out.WriteString(d)
		}
	}

	_, err := io.WriteString(w, out.String())
	if err != nil {
		return nil, fmt.Errorf("writing diff: %w", err)
	}

	return differ, nil
}

// checkRemovable makes sure that dir looks like output of kubenorm: only
// directories and *.yaml files. A typo like "--clean-out-dir -o ~" must not delete a home
// directory.
func checkRemovable(dir string) error {
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() || (d.Type().IsRegular() && filepath.Ext(path) == ".yaml") {
			return nil
		}

		return fmt.Errorf("refusing to remove it, %s is not a YAML file written by kubenorm", path)
	})
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	return fmt.Errorf("--clean-out-dir %s: %w", dir, err)
}

func runCheck(cmd *cobra.Command, flags *normFlags, inputs []string) error {
	normOpts, err := flags.options(cmd.ErrOrStderr())
	if err != nil {
		return err
	}

	resources, err := readInputs(cmd, inputs...)
	if err != nil {
		return err
	}

	// Only the scope filters (CLI flags) limit the raw side, so every change
	// and every resource dropped by the config shows up in the diff.
	cli, err := flags.cliConfig()
	if err != nil {
		return err
	}

	rawOpts, err := flags.scopeOptions(cli)
	if err != nil {
		return err
	}

	// Process modifies the objects in place, so keep a copy of the input.
	clones := normalize.CloneResources(resources)

	kept, err := normOpts.Process(resources)
	if err != nil {
		return err
	}

	// Compare file by file: separate trees (one per cluster, say) may
	// legitimately contain the same resource.
	sourceDiffs, err := diffBySource(checkPairs(resources, clones, kept, rawOpts), func(source string) labels {
		return func(rel string) (string, string) {
			return diffLabel(source), "normalized: " + rel
		}
	})
	if err != nil {
		return err
	}

	notNormalized, err := writeSourceDiffs(cmd.OutOrStdout(), sourceDiffs)
	if err != nil {
		return err
	}

	if len(notNormalized) == 0 {
		return nil
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "not normalized: %s\n", strings.Join(notNormalized, " "))

	return errDifferences
}

func diffLabel(input string) string {
	if input == normalize.StdinPath {
		return "stdin"
	}

	return input
}

func buildDiffCmd() *cobra.Command {
	var (
		flags  normFlags
		full   bool
		output string
	)

	cmd := &cobra.Command{
		Use:   "diff <old> <new>",
		Short: "Normalize both sides and show a unified diff",
		Long: `Normalize both sides (file, directory or "-" for stdin) with the same
options and print a unified diff. Resources are paired by identity
(namespace, group, kind, name), not by file name, so differently organized
trees can be compared. Each side must contain a resource only once, so
compare one cluster at a time. A resource that exists on one side only is
reported with one "Only here (old|new): <file>" line (plus "(<path>)" if
the file holds several resources or is stdin), followed by the tools that
seem to manage it: helm, argocd, flux, fleet/wrangler, rancher, kapp,
kubectl apply, the rendered-by annotation, app.kubernetes.io/managed-by,
the controller owner and managedFields managers. The ---/+++ headers of a changed resource
name the files it was read from (<file>/<path> for stdin or a file with
several resources).

--full shows the content of one-sided resources instead and labels every
resource with a/<normalized path> and b/<normalized path>, in the format of
"git apply" and "patch -p1". It includes the header comment of files where
norm -o writes them, so the patch applies to a tree written by norm -o
(run it in the old directory).

--output json writes one JSON object {"version": 1, "resources": [...]}
with one entry per differing resource (path, status, the file and managers
of each side, the unified diff) for scripts.

--output stat writes a summary like "git diff --stat": one line per
differing resource with its status (A added, D deleted, M modified), path
and number of changed lines, and a total.

Exits 1 if there are differences.

Examples:
  kubenorm diff zz_generated-main/tc zz_generated-pr/tc
  helm template ./chart | kubenorm diff deployed-dump/ -`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDiff(cmd, &flags, args[0], args[1], full, output)
		},
	}

	flags.register(cmd)
	cmd.Flags().StringVar(&output, "output", outputText, "Output format: text (unified diff), stat (summary per resource) or json (for scripts: {\"version\": 1, \"resources\": [...]} with one entry per differing resource)")
	cmd.Flags().BoolVar(&full, "full", false, "Show the whole content of resources that exist on one side only, as a diff from or to /dev/null (git apply compatible output)")

	return cmd
}

// readDiffSide reads one side of kubenorm diff.
func readDiffSide(cmd *cobra.Command, opts *normalize.Options, input string) (diffSide, error) {
	resources, err := readInputs(cmd, input)
	if err != nil {
		return diffSide{}, err
	}

	return newDiffSide(resources, opts)
}

// checkDiffOutput validates --output and its combination with --full.
func checkDiffOutput(output string, full bool) error {
	switch output {
	case outputText, outputJSON:
	case outputStat:
		if full {
			return errors.New("--full and --output stat exclude each other")
		}
	default:
		return fmt.Errorf("--output %q: want %s, %s or %s", output, outputText, outputStat, outputJSON)
	}

	return nil
}

// runDiff implements "kubenorm diff".
func runDiff(cmd *cobra.Command, flags *normFlags, oldPath, newPath string, full bool, output string) error {
	if oldPath == normalize.StdinPath && newPath == normalize.StdinPath {
		return errors.New("only one side can be read from stdin")
	}

	err := checkDiffOutput(output, full)
	if err != nil {
		return err
	}

	opts, err := flags.options(cmd.ErrOrStderr())
	if err != nil {
		return err
	}

	oldSide, err := readDiffSide(cmd, opts, oldPath)
	if err != nil {
		return err
	}

	newSide, err := readDiffSide(cmd, opts, newPath)
	if err != nil {
		return err
	}

	// --full is for git apply: label both sides with the normalized path
	// below the argument, so the names match after -p1. Otherwise name the
	// files the resources come from.
	labels := originLabels(oldSide, newSide)

	var headers *fileHeaders

	if full {
		// a/ and b/ like git: -p1 strips them, whatever the arguments are.
		labels = prefixLabels("a", "b")
		// The header comments of norm -o files are part of the patch, so
		// that it applies to them.
		headers = &fileHeaders{old: oldSide.headerFunc(oldPath), new: newSide.headerFunc(newPath)}
	}

	diffs := compareRendered(oldSide.rendered, newSide.rendered, labels, full, headers)

	switch output {
	case outputJSON:
		err = writeDiffJSON(cmd.OutOrStdout(), diffs, oldSide, newSide)
	case outputStat:
		err = writeDiffStat(cmd.OutOrStdout(), computeStats(diffs, oldSide.rendered, newSide.rendered))
	default:
		err = writeDiffText(cmd.OutOrStdout(), diffs, oldSide, newSide)
	}

	if err != nil {
		return err
	}

	if len(diffs) == 0 {
		return nil
	}

	return errDifferences
}

func buildShowCommonConfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show-common-config",
		Short: "Print the embedded common config",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := cmd.OutOrStdout().Write(normalize.CommonConfig())
			if err != nil {
				return fmt.Errorf("writing common config: %w", err)
			}

			return nil
		},
	}
}

func buildVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), getBuildVersion())
			return nil
		},
	}
}

func buildGendocsCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "gendocs",
		Short:  "Generate usage.md from the command tree",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			b := &bytes.Buffer{}

			err := cobradoc.WriteDocument(b, cmd.Root(), cobradoc.Markdown, cobradoc.Options{})
			if err != nil {
				return fmt.Errorf("generating docs: %w", err)
			}

			err = os.WriteFile("usage.md", b.Bytes(), 0o600)
			if err != nil {
				return fmt.Errorf("writing usage.md: %w", err)
			}

			return nil
		},
	}
}

func getBuildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}

	return info.Main.Version
}
