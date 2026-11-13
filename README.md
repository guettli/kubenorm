# kubenorm: normalize Kubernetes YAML for clean diffs

`kubenorm` normalizes Kubernetes manifests on disk so that two YAML
representations of the same desired state become byte-identical. It never
connects to a cluster.

It is made for the [Rendered Manifests Pattern](https://akuity.io/blog/the-rendered-manifests-pattern)
(RMP): render helm charts, kustomize overlays and plain manifests, normalize
the result, commit it, and review the real change in every pull request.

What normalization does:

- removes server-managed fields (`status`, `managedFields`, `uid`,
  `resourceVersion`, `creationTimestamp`, ...)
- removes values equal to the Kubernetes defaults (`imagePullPolicy: IfNotPresent`,
  `protocol: TCP`, `revisionHistoryLimit: 10`, probe defaults, ...)
- removes tool noise (`helm.sh/chart` labels, kapp/argocd tracking labels,
  `last-applied-configuration`, ...)
- drops noise resources (Pods, Events, ControllerRevisions, cert-manager
  CertificateRequests, ...)
- sorts keys and writes every resource to its own stable path

```text
NAMESPACE/[GROUP_]KIND/NAME.yaml        e.g. demo/apps_Deployment/web.yaml
_cluster/[GROUP_]KIND/NAME.yaml         cluster-scoped, e.g. _cluster/Namespace/demo.yaml
```

The rules live in an [embedded common config](normalize/common-config.yaml)
(`kubenorm show-common-config`) and can be extended with your own config files.

## Install

```terminal
go run github.com/guettli/kubenorm@latest --help
```

or download a binary from the [releases](https://github.com/guettli/kubenorm/releases).

## `kubenorm norm`

Reads a file, a directory (recursive, `*.yaml` / `*.yml`) or stdin (`-`).

```terminal
# helm output -> normalized tree, one file per resource
helm template my-release ./chart | kubenorm norm - -o rendered/my-release -r

# normalize to stdout (one multi-document stream, sorted)
kubenorm norm ./manifests

# CI guard: exit 1 and show a diff if hand-written manifests are not normalized
kubenorm norm ./k8s/plain --check
```

`--check` ignores comments, key order and file layout: it only fails if
normalization would change the content.

`--write` (`-w`) fixes the files in place, like `gofmt -w`: exactly the files
`--check` complains about are rewritten. The comment block at the top of a
file is kept, other comments in a rewritten file are lost. Files containing
documents kubenorm does not normalize (no `metadata`, SOPS) are refused
instead of being rewritten.

```terminal
kubenorm norm ./k8s/plain --write
```

### Namespaces of rendered charts

Some charts rely on `helm --namespace` and do not set `metadata.namespace`.
`--add-namespace-if-missing NS` sets it on every namespaced resource without a
namespace, `--overwrite-namespace NS` sets it on all namespaced resources.

```terminal
helm template liqo liqo/liqo --namespace liqo | kubenorm norm - --add-namespace-if-missing liqo -o out/
```

kubenorm has no api-server to ask whether a kind is cluster-scoped. It uses,
in this order:

1. CRDs contained in the input (`spec.scope`),
2. the `clusterScopedResources` section of your config,
3. a built-in list of Kubernetes kinds and common ecosystem CRDs
   (`ClusterIssuer`, `GatewayClass`, ...).

Everything else is treated as namespaced. The namespace is set before
filtering, so `--namespaces` and skip rules see the new value. Only
`metadata.namespace` is changed, references inside objects are not rewritten.

### Secrets

Secret values are kept as they are (the files are your own input). Use
`--redact-secrets` to replace them with a marker, or `--secrets-as-stringdata`
to decode `data` into readable `stringData`.

SOPS-encrypted documents (a top-level `sops` map with a `mac`) are refused:
re-encoding reorders keys and breaks the SOPS MAC. Keep them out of the
input, or pass `--skip-sops` to drop them.

## `kubenorm diff`

Normalizes both sides with the same options and prints a unified diff
(`git apply` compatible). Resources are paired by identity, not by file name,
so differently organized trees, single files and streams can be compared.

```terminal
# What does this PR really change?
kubenorm diff <(git show main:rendered.yaml) rendered.yaml

# Compare a cluster dump (from dumpall) with the desired state
dumpall --read-resource-names-from ./rendered -o current
kubenorm diff current ./rendered
```

Exit codes: `0` no differences, `1` differences, `2` error.

## Config

```yaml
removeFields:
  - group: apps
    kind: Deployment
    namespace: prod-*
    name: api-*
    fields:
      - status
      - metadata.annotations.example\.com/build-id
      - path: spec.replicas        # only remove if the value matches
        value: 1
      - path: metadata.labels      # only remove if empty
        omitempty: true
      - path: spec.ports.targetPort
        omitIfEqualToSibling: port

skipResources:
  - kind: Event
  - group: apps
    kind: Deployment
    namespace: test-*
    name: debug-*

excludeNamespaces:
  - foo-*

clusterScopedResources:
  - group: example.com
    kind: Cluster*
```

- Matchers (`group`, `kind`, `namespace`, `name`) are globs; omitted means `*`.
  Cluster-scoped resources match the namespace `_cluster`.
- Field paths use dots; escape literal dots as `\.`. Segments may use `*` / `?`.
  `...` matches any depth (`...labels.helm\.sh/chart`). Lists are walked
  automatically (`webhooks.clientConfig.caBundle`).
- `excludeNamespaces` drops the Namespace object and everything inside.
- The config is parsed strictly; unknown keys are an error.

`--config FILE` (repeatable) adds your rules to the common config,
`--no-common-config` disables the embedded one.

## Usage

<!-- usage:start -->
```text
kubenorm normalizes Kubernetes YAML manifests on disk.

It removes server-managed and defaulted fields (status, managedFields,
creationTimestamp, default values, tool labels, ...), drops noise resources
and writes every resource to a stable path:

  NAMESPACE/[GROUP_]KIND/NAME.yaml   (cluster-scoped: _cluster/...)

This makes rendered manifests (Rendered Manifests Pattern), helm output and
cluster dumps comparable. kubenorm never connects to a cluster; use dumpall
(https://github.com/guettli/dumpall) to dump live resources.

Exit codes: 0 ok, 1 differences found (diff, norm --check), 2 error.

Usage:
  kubenorm [command]

Available Commands:
  completion         Generate the autocompletion script for the specified shell
  diff               Normalize both sides and show a unified diff
  help               Help about any command
  norm               Normalize YAML manifests
  show-common-config Print the embedded common config
  version            Print the version

Flags:
  -h, --help   help for kubenorm

Use "kubenorm [command] --help" for more information about a command.
```
<!-- usage:end -->

See [usage.md](usage.md) for all subcommands and flags.

## Library

The normalization is a Go package, used by [dumpall](https://github.com/guettli/dumpall)
as well:

```go
import "github.com/guettli/kubenorm/normalize"

cfg, _ := normalize.LoadConfig(true, "")
opts := &normalize.Options{Config: cfg, SkipOwned: true}
resources, _ := normalize.ReadPath("./manifests", os.Stdin)
resources, _ = opts.Process(resources)
rendered, _ := normalize.Render(resources)
_ = rendered.WriteDir("out")
```

## Related

- [dumpall](https://github.com/guettli/dumpall) Dump all resources of a cluster into the same layout.
- [check-conditions](https://github.com/guettli/check-conditions) Check all conditions of all resources in your cluster.
- [korphan](https://github.com/guettli/korphan) Find orphaned Kubernetes resources.
