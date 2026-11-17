// Package normalize normalizes Kubernetes resources so that two YAML
// representations of the same desired state become byte-identical.
//
// It removes server-managed and defaulted fields according to a Config
// (see CommonConfig for the embedded defaults), drops resources matching
// skip rules and writes them in a stable layout:
//
//	NAMESPACE/[GROUP_]KIND/NAME.yaml
//
// Resources without metadata.namespace use the directory "_cluster".
//
// # Data flow
//
//  1. Read: ReadPaths reads files, directories and stdin, ParseYAML splits a
//     stream into one Resource per document (List kinds are expanded,
//     SOPS-encrypted documents are marked).
//  2. Configure: LoadConfig compiles the common config and user files into a
//     Config. Options holds the Config plus the CLI choices (namespace
//     handling, filters, secrets, labels and annotations to set). WithCRDs adds CRDs that are not part of
//     the input.
//  3. Process: Options.Process sets namespaces, drops filtered and
//     SOPS-encrypted resources and normalizes the rest in place. Explain
//     does the same and records every change.
//  4. Write: Render encodes the resources like kubectl and maps them to
//     their paths (two resources on one path are an error). Rendered.WriteDir
//     writes one file per resource and never overwrites a file;
//     Rendered.WriteStream writes one multi-document stream.
//
// # Files
//
//   - resource.go: Resource, reading, Process, Render, writing.
//   - config.go: the config format, LoadConfig, rule compilation.
//   - common-config.yaml: the embedded common config.
//   - normalize.go: Options and the per-resource normalization.
//   - fields.go: field paths with globs, removing fields.
//   - filter.go: skip rules, --kind, --name-regex, --namespaces, SkipOwned.
//   - namespace.go: scope of kinds (built-in, CRDs, config) and namespace
//     handling, including references to namespaces in other objects.
//   - quantity.go: canonical quantities (1000m becomes "1").
//   - secrets.go: redacting Secrets or converting data to stringData.
//   - marshal.go: YAML output with the key order of kubectl.
//   - explain.go: the trace behind Explain.
//   - parallel.go: the worker pool used by ReadPaths and Process.
package normalize
