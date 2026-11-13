// Command kubenorm normalizes Kubernetes YAML manifests on disk, so that two
// representations of the same desired state become byte-identical. It never
// connects to a cluster.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/gavv/cobradoc"
	"github.com/guettli/kubenorm/normalize"
	"github.com/spf13/cobra"
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

	root.AddCommand(buildNormCmd(), buildDiffCmd(), buildShowCommonConfigCmd(), buildVersionCmd(), buildGendocsCmd())

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
	skipSOPS              bool
}

func (f *normFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringArrayVarP(&f.configFiles, "config", "c", nil, "YAML config with removeFields/skipResources/excludeNamespaces/clusterScopedResources rules (repeatable; merged with the common config)")
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
	fl.StringVar(&f.addNamespaceIfMissing, "add-namespace-if-missing", "", "Set metadata.namespace on namespaced resources without namespace (cluster-scoped kinds are left alone)")
	fl.StringVar(&f.overwriteNamespace, "overwrite-namespace", "", "Set metadata.namespace on all namespaced resources, replacing existing values")
	fl.BoolVar(&f.skipSOPS, "skip-sops", false, "Drop SOPS-encrypted documents instead of failing (normalizing them would break the SOPS MAC)")
}

// cliConfig returns the rules that come from command-line flags only
// (--skip-name-glob, --exclude-namespaces). They restrict the scope and are
// applied to both sides of norm --check.
func (f *normFlags) cliConfig() (*normalize.Config, error) {
	cfg := &normalize.Config{}

	err := cfg.AddSkipNameGlob(f.skipNameGlob)
	if err != nil {
		return nil, err
	}

	err = cfg.AddExcludeNamespacesCSV(f.excludeNamespacesCSV)
	if err != nil {
		return nil, err
	}

	return cfg, nil
}

func (f *normFlags) options() (*normalize.Options, error) {
	cfg, err := normalize.LoadConfig(!f.noCommonConfig, "")
	if err != nil {
		return nil, err
	}

	for _, file := range f.configFiles {
		user, err := normalize.LoadConfig(false, file)
		if err != nil {
			return nil, err
		}

		cfg.Merge(user)
	}

	cli, err := f.cliConfig()
	if err != nil {
		return nil, err
	}

	cfg.Merge(cli)

	opts, err := f.scopeOptions(cfg)
	if err != nil {
		return nil, err
	}

	opts.KeepManagedFields = f.keepManagedFields
	opts.RedactSecrets = f.redactSecrets
	opts.SecretsAsStringData = f.secretsAsStringData
	opts.AddNamespaceIfMissing = strings.TrimSpace(f.addNamespaceIfMissing)
	opts.OverwriteNamespace = strings.TrimSpace(f.overwriteNamespace)

	err = opts.Validate()
	if err != nil {
		return nil, err
	}

	return opts, nil
}

// scopeOptions returns options with only the filters set.
func (f *normFlags) scopeOptions(cfg *normalize.Config) (*normalize.Options, error) {
	namespaces, err := normalize.ParseNamespacesCSV(f.namespacesCSV)
	if err != nil {
		return nil, err
	}

	kinds, err := normalize.ParseKindsCSV(f.kindsCSV)
	if err != nil {
		return nil, err
	}

	nameRegex, err := normalize.ParseNameRegex(f.nameRegex)
	if err != nil {
		return nil, err
	}

	return &normalize.Options{
		Config:     cfg,
		Namespaces: namespaces,
		Kinds:      kinds,
		NameRegex:  nameRegex,
		SkipOwned:  f.skipOwned,
		SkipSOPS:   f.skipSOPS,
	}, nil
}

func buildNormCmd() *cobra.Command {
	var (
		flags        normFlags
		outDir       string
		removeOutDir bool
		check        bool
		comment      string
		verbose      bool
	)

	cmd := &cobra.Command{
		Use:   "norm <file|dir|->",
		Short: "Normalize YAML manifests",
		Long: `Normalize YAML manifests from a file, a directory (recursive) or stdin ("-").

Without --out-dir the result is written to stdout as one multi-document YAML
stream, sorted by NAMESPACE/KIND/NAME. With --out-dir every resource is
written to its own file NAMESPACE/[GROUP_]KIND/NAME.yaml.

With --check nothing is written: kubenorm shows what normalization would
change and exits 1 if anything would change. Comments, key order and the file
layout are ignored by --check.

Examples:
  helm template my-chart | kubenorm norm - -o rendered/
  kubenorm norm ./k8s/plain --check
  kubenorm norm liqo.yaml --add-namespace-if-missing liqo -o out/`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if check && outDir != "" {
				return errors.New("--check and --out-dir are mutually exclusive")
			}

			if check {
				return runCheck(cmd, &flags, args[0])
			}

			return runNorm(cmd, &flags, args[0], outDir, removeOutDir, comment, verbose)
		},
	}

	flags.register(cmd)
	cmd.Flags().StringVarP(&outDir, "out-dir", "o", "", "Write one file per resource into this directory (must not exist, see --remove-out-dir)")
	cmd.Flags().BoolVarP(&removeOutDir, "remove-out-dir", "r", false, "Remove --out-dir before writing")
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

// inputReader returns a function that reads the input resources. stdin is
// buffered, so the function can be called more than once.
func inputReader(cmd *cobra.Command, input string) (func() ([]normalize.Resource, error), error) {
	if input != normalize.StdinPath {
		return func() ([]normalize.Resource, error) {
			return normalize.ReadPath(input, nil)
		}, nil
	}

	data, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return nil, fmt.Errorf("failed to read stdin: %w", err)
	}

	return func() ([]normalize.Resource, error) {
		return normalize.ParseYAML(data, normalize.StdinPath)
	}, nil
}

func readAndProcess(cmd *cobra.Command, opts *normalize.Options, input string) (normalize.Rendered, error) {
	read, err := inputReader(cmd, input)
	if err != nil {
		return nil, err
	}

	return processRendered(read, opts)
}

func processRendered(read func() ([]normalize.Resource, error), opts *normalize.Options) (normalize.Rendered, error) {
	resources, err := read()
	if err != nil {
		return nil, err
	}

	resources, err = opts.Process(resources)
	if err != nil {
		return nil, err
	}

	return normalize.Render(resources)
}

func runNorm(cmd *cobra.Command, flags *normFlags, input, outDir string, removeOutDir bool, comment string, verbose bool) error {
	opts, err := flags.options()
	if err != nil {
		return err
	}

	resources, err := normalize.ReadPath(input, cmd.InOrStdin())
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

	err = prepareOutDir(outDir, removeOutDir)
	if err != nil {
		return err
	}

	err = rendered.WriteDir(outDir)
	if err != nil {
		return err
	}

	if verbose {
		for _, rel := range rendered.SortedPaths() {
			fmt.Fprintf(cmd.ErrOrStderr(), "Written: %s/%s\n", outDir, rel)
		}
	}

	return nil
}

func prepareOutDir(outDir string, remove bool) error {
	if remove {
		err := os.RemoveAll(outDir)
		if err != nil {
			return fmt.Errorf("failed to remove out-dir %s: %w", outDir, err)
		}
	}

	_, err := os.Stat(outDir)
	if err == nil {
		return fmt.Errorf("output directory %q already exists, use --remove-out-dir to replace it", outDir)
	}

	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to inspect output directory %q: %w", outDir, err)
	}

	return nil
}

func runCheck(cmd *cobra.Command, flags *normFlags, input string) error {
	normOpts, err := flags.options()
	if err != nil {
		return err
	}

	cli, err := flags.cliConfig()
	if err != nil {
		return err
	}

	// The raw side only applies the scope filters, so every change made by
	// normalization shows up in the diff.
	rawOpts, err := flags.scopeOptions(cli)
	if err != nil {
		return err
	}

	rawOpts.KeepManagedFields = true

	read, err := inputReader(cmd, input)
	if err != nil {
		return err
	}

	raw, err := processRendered(read, rawOpts)
	if err != nil {
		return err
	}

	normalized, err := processRendered(read, normOpts)
	if err != nil {
		return err
	}

	result, err := normalize.Diff(cmd.OutOrStdout(), raw, normalized, diffLabel(input), "normalized")
	if err != nil {
		return err
	}

	if result.Equal() {
		return nil
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "%s is not normalized: %d changed, %d dropped, %d added\n",
		input, len(result.Changed), len(result.Removed), len(result.Added))

	return errDifferences
}

func diffLabel(input string) string {
	if input == normalize.StdinPath {
		return "stdin"
	}

	return input
}

func buildDiffCmd() *cobra.Command {
	var flags normFlags

	cmd := &cobra.Command{
		Use:   "diff <old> <new>",
		Short: "Normalize both sides and show a unified diff",
		Long: `Normalize both sides (file, directory or "-" for stdin) with the same
options and print a unified diff. Resources are paired by identity
(namespace, group, kind, name), not by file name, so differently organized
trees can be compared. Exits 1 if there are differences.

Examples:
  kubenorm diff zz_generated-main/ zz_generated-pr/
  helm template ./chart | kubenorm diff deployed-dump/ -`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] == normalize.StdinPath && args[1] == normalize.StdinPath {
				return errors.New("only one side can be read from stdin")
			}

			opts, err := flags.options()
			if err != nil {
				return err
			}

			oldSide, err := readAndProcess(cmd, opts, args[0])
			if err != nil {
				return err
			}

			newSide, err := readAndProcess(cmd, opts, args[1])
			if err != nil {
				return err
			}

			result, err := normalize.Diff(cmd.OutOrStdout(), oldSide, newSide, diffLabel(args[0]), diffLabel(args[1]))
			if err != nil {
				return err
			}

			if result.Equal() {
				return nil
			}

			return errDifferences
		},
	}

	flags.register(cmd)

	return cmd
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
