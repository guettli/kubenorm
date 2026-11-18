# Kubenorm Manual

kubenorm normalizes Kubernetes YAML manifests on disk.

It removes server-managed and defaulted fields (status, managedFields,
creationTimestamp, default values, tool labels, ...), drops noise resources
and writes every resource to a stable path:

  NAMESPACE/[GROUP_]KIND/NAME.yaml   (cluster-scoped: _cluster/...)

This makes rendered manifests (Rendered Manifests Pattern), helm output and
cluster dumps comparable. kubenorm never connects to a cluster; use dumpall
(https://github.com/guettli/dumpall) to dump live resources.

Exit codes: 0 ok, 1 differences found (diff, norm --check), 2 error.

```text
kubenorm [command] [global flags] [command flags]
```

### Commands
- [kubenorm completion](#kubenorm-completion)
- [kubenorm completion bash](#kubenorm-completion-bash)
- [kubenorm completion fish](#kubenorm-completion-fish)
- [kubenorm completion help](#kubenorm-completion-help)
- [kubenorm completion powershell](#kubenorm-completion-powershell)
- [kubenorm completion zsh](#kubenorm-completion-zsh)
- [kubenorm diff](#kubenorm-diff)
- [kubenorm explain](#kubenorm-explain)
- [kubenorm help](#kubenorm-help)
- [kubenorm norm](#kubenorm-norm)
- [kubenorm show-common-config](#kubenorm-show-common-config)
- [kubenorm version](#kubenorm-version)


# Commands

## `kubenorm completion`

Generate the autocompletion script for kubenorm for the specified shell.
See each sub-command's help for details on how to use the generated script.


```text
kubenorm completion [flags]
```

### Command Flags

```text
  -h, --help   help for completion
```

## `kubenorm completion bash`

Generate the autocompletion script for the bash shell.

This script depends on the 'bash-completion' package.
If it is not installed already, you can install it via your OS's package manager.

To load completions in your current shell session:

	source <(kubenorm completion bash)

To load completions for every new session, execute once:

#### Linux:

	kubenorm completion bash > /etc/bash_completion.d/kubenorm

#### macOS:

	kubenorm completion bash > $(brew --prefix)/etc/bash_completion.d/kubenorm

You will need to start a new shell for this setup to take effect.


```text
kubenorm completion bash
```

### Command Flags

```text
  -h, --help              help for bash
      --no-descriptions   disable completion descriptions
```

## `kubenorm completion fish`

Generate the autocompletion script for the fish shell.

To load completions in your current shell session:

	kubenorm completion fish | source

To load completions for every new session, execute once:

	kubenorm completion fish > ~/.config/fish/completions/kubenorm.fish

You will need to start a new shell for this setup to take effect.


```text
kubenorm completion fish [flags]
```

### Command Flags

```text
  -h, --help              help for fish
      --no-descriptions   disable completion descriptions
```

## `kubenorm completion help`

Help provides help for any command in the application.
Simply type completion help [path to command] for full details.

```text
kubenorm completion help [command] [flags]
```

### Command Flags

```text
  -h, --help   help for help
```

## `kubenorm completion powershell`

Generate the autocompletion script for powershell.

To load completions in your current shell session:

	kubenorm completion powershell | Out-String | Invoke-Expression

To load completions for every new session, add the output of the above command
to your powershell profile.


```text
kubenorm completion powershell [flags]
```

### Command Flags

```text
  -h, --help              help for powershell
      --no-descriptions   disable completion descriptions
```

## `kubenorm completion zsh`

Generate the autocompletion script for the zsh shell.

If shell completion is not already enabled in your environment you will need
to enable it.  You can execute the following once:

	echo "autoload -U compinit; compinit" >> ~/.zshrc

To load completions in your current shell session:

	source <(kubenorm completion zsh)

To load completions for every new session, execute once:

#### Linux:

	kubenorm completion zsh > "${fpath[1]}/_kubenorm"

#### macOS:

	kubenorm completion zsh > $(brew --prefix)/share/zsh/site-functions/_kubenorm

You will need to start a new shell for this setup to take effect.


```text
kubenorm completion zsh [flags]
```

### Command Flags

```text
  -h, --help              help for zsh
      --no-descriptions   disable completion descriptions
```

## `kubenorm diff`

Normalize both sides (file, directory or "-" for stdin) with the same
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
resource with <argument>/<normalized path>, in the format of "git apply"
and "patch -p1". It includes the comment lines at the top of files that
hold one resource, so the patch applies to trees written by norm -o.

--output json writes one JSON object {"version": 1, "resources": [...]}
with one entry per differing resource (path, status, the file and managers
of each side, the unified diff) for scripts.

Exits 1 if there are differences.

Examples:
  kubenorm diff zz_generated-main/tc zz_generated-pr/tc
  helm template ./chart | kubenorm diff deployed-dump/ -

```text
kubenorm diff <old> <new> [flags]
```

### Command Flags

```text
      --add-namespace-if-missing string   Set metadata.namespace on namespaced resources without namespace, and fill empty namespace references (cluster-scoped kinds are left alone)
  -c, --config stringArray                YAML config with removeFields/skipResources/excludeNamespaces/quantityFields/clusterScopedResources/setMetadata/ownerHints/keepFields rules (repeatable; merged with the common config)
      --crds stringArray                  Also read CustomResourceDefinitions from this file or directory (repeatable; other resources there are ignored): their quantity fields, schema defaults and scope are used like CRDs in the input
      --exclude-namespaces string         Drop these namespaces (globs, comma-separated): the Namespace object and everything inside
      --full                              Show the whole content of resources that exist on one side only, as a diff from or to /dev/null (git apply compatible output)
  -h, --help                              help for diff
      --keep-managed-fields               Keep metadata.managedFields
      --kind string                       Only keep kinds matching these globs (comma-separated, e.g. 'ConfigMap,Cluster*')
  -x, --name-regex string                 Only keep resources whose metadata.name matches this regex
  -n, --namespaces string                 Only keep resources in these namespaces (comma-separated)
      --no-common-config                  Do not apply the embedded common config (see show-common-config)
      --no-config-discovery               Do not load .kubenorm.yaml from the current directory or its parents
      --output string                     Output format: text (unified diff) or json (for scripts: {"version": 1, "resources": [...]} with one entry per differing resource) (default "text")
      --overwrite-namespace string        Set metadata.namespace on all namespaced resources, replacing existing values, and move references to the replaced namespaces
      --redact-secrets                    Replace values of Secrets with REDACTED
      --rendered-by string                Set the annotation kubenorm.guettli.github.io/rendered-by to this value (e.g. the script that renders the manifests) on every resource
      --secrets-as-stringdata             Convert base64 Secret data to stringData
      --set-annotation stringArray        Set this annotation (key=value, repeatable) on every resource, replacing an existing value
      --set-label stringArray             Set this label (key=value, repeatable) on every resource, replacing an existing value
      --skip-name-glob string             Drop resources whose metadata.name matches this anchored glob
  -O, --skip-owned                        Drop resources with a controlling ownerReference and objects Kubernetes autogenerates
```

## `kubenorm explain`

Normalize like "norm" and print, for every resource, each change and the
rule or option that made it. Useful to write and debug your own config.

  - removed field     + added field     ~ changed value

Example:
  kubenorm explain deployment.yaml --config my-rules.yaml

```text
kubenorm explain <file|dir|->... [flags]
```

### Command Flags

```text
      --add-namespace-if-missing string   Set metadata.namespace on namespaced resources without namespace, and fill empty namespace references (cluster-scoped kinds are left alone)
  -c, --config stringArray                YAML config with removeFields/skipResources/excludeNamespaces/quantityFields/clusterScopedResources/setMetadata/ownerHints/keepFields rules (repeatable; merged with the common config)
      --crds stringArray                  Also read CustomResourceDefinitions from this file or directory (repeatable; other resources there are ignored): their quantity fields, schema defaults and scope are used like CRDs in the input
      --exclude-namespaces string         Drop these namespaces (globs, comma-separated): the Namespace object and everything inside
  -h, --help                              help for explain
      --keep-managed-fields               Keep metadata.managedFields
      --kind string                       Only keep kinds matching these globs (comma-separated, e.g. 'ConfigMap,Cluster*')
  -x, --name-regex string                 Only keep resources whose metadata.name matches this regex
  -n, --namespaces string                 Only keep resources in these namespaces (comma-separated)
      --no-common-config                  Do not apply the embedded common config (see show-common-config)
      --no-config-discovery               Do not load .kubenorm.yaml from the current directory or its parents
      --overwrite-namespace string        Set metadata.namespace on all namespaced resources, replacing existing values, and move references to the replaced namespaces
      --redact-secrets                    Replace values of Secrets with REDACTED
      --rendered-by string                Set the annotation kubenorm.guettli.github.io/rendered-by to this value (e.g. the script that renders the manifests) on every resource
      --secrets-as-stringdata             Convert base64 Secret data to stringData
      --set-annotation stringArray        Set this annotation (key=value, repeatable) on every resource, replacing an existing value
      --set-label stringArray             Set this label (key=value, repeatable) on every resource, replacing an existing value
      --skip-name-glob string             Drop resources whose metadata.name matches this anchored glob
  -O, --skip-owned                        Drop resources with a controlling ownerReference and objects Kubernetes autogenerates
```

## `kubenorm help`

Help provides help for any command in the application.
Simply type kubenorm help [path to command] for full details.

```text
kubenorm help [command] [flags]
```

### Command Flags

```text
  -h, --help   help for help
```

## `kubenorm norm`

Normalize YAML manifests from files, directories (recursive) or stdin ("-").

Without --out-dir the result is written to stdout as one multi-document YAML
stream, sorted by NAMESPACE/KIND/NAME. With --out-dir every resource is
written to its own file NAMESPACE/[GROUP_]KIND/NAME.yaml.

With --check nothing is written: kubenorm shows what normalization would
change and exits 1 if anything would change. Comments, key order and the file
layout are ignored by --check.

Examples:
  helm template my-chart | kubenorm norm - -o zz_rendered/
  kubenorm norm ./k8s/plain --check
  kubenorm norm liqo.yaml --add-namespace-if-missing liqo -o out/

```text
kubenorm norm <file|dir|->... [flags]
```

### Command Flags

```text
      --add-namespace-if-missing string   Set metadata.namespace on namespaced resources without namespace, and fill empty namespace references (cluster-scoped kinds are left alone)
      --check                             Do not write; exit 1 and show a diff if the input is not normalized
      --clean-out-dir                     Remove --out-dir before writing (only if it contains nothing but *.yaml files)
      --comment string                    Additional comment line at the top of each output file
  -c, --config stringArray                YAML config with removeFields/skipResources/excludeNamespaces/quantityFields/clusterScopedResources/setMetadata/ownerHints/keepFields rules (repeatable; merged with the common config)
      --crds stringArray                  Also read CustomResourceDefinitions from this file or directory (repeatable; other resources there are ignored): their quantity fields, schema defaults and scope are used like CRDs in the input
      --exclude-namespaces string         Drop these namespaces (globs, comma-separated): the Namespace object and everything inside
  -h, --help                              help for norm
      --keep-managed-fields               Keep metadata.managedFields
      --kind string                       Only keep kinds matching these globs (comma-separated, e.g. 'ConfigMap,Cluster*')
  -x, --name-regex string                 Only keep resources whose metadata.name matches this regex
  -n, --namespaces string                 Only keep resources in these namespaces (comma-separated)
      --no-common-config                  Do not apply the embedded common config (see show-common-config)
      --no-config-discovery               Do not load .kubenorm.yaml from the current directory or its parents
  -o, --out-dir string                    Write one file per resource into this directory (must not exist, see --clean-out-dir)
      --overwrite-namespace string        Set metadata.namespace on all namespaced resources, replacing existing values, and move references to the replaced namespaces
      --redact-secrets                    Replace values of Secrets with REDACTED
      --rendered-by string                Set the annotation kubenorm.guettli.github.io/rendered-by to this value (e.g. the script that renders the manifests) on every resource
      --secrets-as-stringdata             Convert base64 Secret data to stringData
      --set-annotation stringArray        Set this annotation (key=value, repeatable) on every resource, replacing an existing value
      --set-label stringArray             Set this label (key=value, repeatable) on every resource, replacing an existing value
      --skip-name-glob string             Drop resources whose metadata.name matches this anchored glob
  -O, --skip-owned                        Drop resources with a controlling ownerReference and objects Kubernetes autogenerates
  -v, --verbose                           Print every written file to stderr
```

## `kubenorm show-common-config`

Print the embedded common config

```text
kubenorm show-common-config [flags]
```

### Command Flags

```text
  -h, --help   help for show-common-config
```

## `kubenorm version`

Print the version

```text
kubenorm version [flags]
```

### Command Flags

```text
  -h, --help   help for version
```
