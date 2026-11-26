# kubenorm: normalize Kubernetes YAML for clean diffs

`kubenorm` normalizes Kubernetes manifests on disk so that two YAML
representations of the same desired state become byte-identical. It never
connects to a cluster. To dump the live resources of a cluster into the same
layout, use [dumpall](https://github.com/guettli/dumpall), which uses kubenorm
as a library.

It is made for the [Rendered Manifests Pattern](#rendered-manifests-pattern)
(RMP): render Helm charts, kustomize overlays and plain manifests, normalize
the result, commit it, and review the real change in every pull request.

What normalization does:

- removes server-managed fields (`status`, `managedFields`, `uid`,
  `resourceVersion`, `creationTimestamp`, ...)
- removes values equal to the Kubernetes defaults (`imagePullPolicy: IfNotPresent`,
  `protocol: TCP`, `revisionHistoryLimit: 10`, probe defaults, ...)
- writes quantities in the canonical form of the api-server
  (`cpu: 1000m` → `cpu: "1"`, `memory: 1024Mi` → `memory: 1Gi`, `cpu: 0.5` → `cpu: 500m`)
- removes tool noise (`helm.sh/chart` labels, kapp/argocd tracking labels,
  `last-applied-configuration`, restart timestamps, sync bookkeeping, ...)
- drops noise resources (Pods, Events, ControllerRevisions, cert-manager
  CertificateRequests, ...)
- sorts keys and writes every resource to its own stable path

```text
NAMESPACE/[GROUP_]KIND/NAME.yaml        e.g. demo/apps_Deployment/web.yaml
_cluster/[GROUP_]KIND/NAME.yaml         without metadata.namespace, e.g. _cluster/Namespace/demo.yaml
```

kubenorm never asks an api-server, so every resource without
`metadata.namespace` goes to `_cluster/`, also one of a namespaced kind (see
`--add-namespace-if-missing`).

The rules live in an [embedded common config](normalize/common-config.yaml)
(`kubenorm show-common-config`) and can be extended with your own config files.

## Install

With [mise](https://mise.jdx.dev), pinned to the latest release:

```terminal
mise use --pin github:guettli/kubenorm@latest
```

This writes the exact version to `mise.toml`, so your laptop and CI use the
same kubenorm. `mise upgrade --bump github:guettli/kubenorm` moves to a newer
release. mise skips releases younger than 24 hours (`minimum_release_age`),
so `@latest` can be one release behind for a day. For `render.sh` and CI pin
helm the same way: `mise use --pin helm@latest`.

Alternatives: download a binary from the
[releases](https://github.com/guettli/kubenorm/releases), or
`go run github.com/guettli/kubenorm@latest` (needs Go).

## Rendered Manifests Pattern

With the [Rendered Manifests Pattern](https://akuity.io/blog/the-rendered-manifests-pattern)
nothing is templated at deploy time. A script renders everything into plain
YAML, the result is committed, and your favorite GitOps tool (Flux, Argo CD,
Rancher Fleet, ...) applies exactly these files:

1. `render.sh` runs `helm template` (or `kustomize build`), which writes one
   big multi-document YAML stream to stdout.
2. kubenorm reads the stream, sets the namespace if it is missing, normalizes
   every resource and writes it to its own file below `zz_rendered/`.
3. `zz_rendered/` is committed to git, together with the change of the chart
   version or the values.
4. The GitOps tool reads `zz_rendered/`, not the chart.

```bash
#!/usr/bin/env bash
# render.sh: render all charts and manifests into zz_rendered/
set -euo pipefail

{
  helm template cert-manager cert-manager --repo https://charts.jetstack.io \
    --version v1.21.3 --namespace cert-manager -f values/cert-manager.yaml |
    kubenorm norm - --add-namespace-if-missing cert-manager

  kubenorm norm k8s/plain
} | kubenorm norm --clean-out-dir -o zz_rendered -
```

The last call writes the whole tree. `--clean-out-dir` deletes the old
`zz_rendered/` first, so every file is created from scratch. This is
important: otherwise the files of deleted resources stay in `zz_rendered/`,
the pull request does not show the deletion, and the GitOps tool keeps the
resources. To protect you from a typo, kubenorm removes only a directory that
contains nothing but `*.yaml` files.

Every document on stdout starts with `---`, so the output of several
`kubenorm norm` calls can be piped into one. Two charts that render the same
resource fail the last call.

```yaml
# For example an Argo CD Application
spec:
  source:
    repoURL: https://github.com/example/gitops
    path: zz_rendered
    directory:
      recurse: true
```

No surprises anymore: every change is visible in the GitOps pull request, for
example a chart update that adds a Deployment, a values change that drops a
NetworkPolicy, or a new chart default. kubenorm removes the noise
(`helm.sh/chart` labels, defaults, key order), so the diff of `zz_rendered/`
shows only real changes.

`helm template` does not talk to a cluster. It does not know the Kubernetes
version or the APIs of your cluster, and `lookup` returns nothing:

- Pass `--kube-version 1.34.0` to helm if a chart checks
  `.Capabilities.KubeVersion` (for example to choose an API version).
- Pass `--api-versions monitoring.coreos.com/v1` (repeatable) to helm if a
  chart checks `.Capabilities.APIVersions`. Without it, such a chart silently
  leaves out its `ServiceMonitor`, although your cluster has the CRD.
- If a chart uses `lookup` (for example to reuse an existing Secret), set the
  value in your values file instead.

### Check in CI

Render in CI and fail the pull request if the committed `zz_rendered/` is not
up to date. `jdx/mise-action` installs the kubenorm version of your
`mise.toml`:

```yaml
# .github/workflows/rendered.yml
name: rendered
on: pull_request
jobs:
  rendered:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: jdx/mise-action@v5 # installs the tools of mise.toml: kubenorm, helm
      - run: ./render.sh
      - name: zz_rendered/ is up to date
        run: |
          git add --intent-to-add zz_rendered
          git diff --exit-code --stat zz_rendered || {
            echo "Run ./render.sh and commit zz_rendered/"; exit 1; }
```

Every written file starts with a header naming the kubenorm version, so a
render with another version shows up in the diff.

### Migrate to the Rendered Manifests Pattern

You start with resources in the cluster that were applied by hand
(`kubectl apply`, `helm install`) or rendered at deploy time (an Argo CD
Application with a Helm chart as source, a Flux HelmRelease). At the end the
same resources come from `zz_rendered/`, and nothing in the cluster was
recreated.

1. Install the tools:

   ```terminal
   mise use --pin github:guettli/kubenorm@latest github:guettli/dumpall@latest helm@latest
   ```

2. Collect what is deployed now. For each Helm release take the chart and
   version from `helm list -A` and the values from
   `helm get values RELEASE -n NS -o yaml > values/RELEASE.yaml` (this works
   for `helm install` and Flux). For Argo CD take chart, version and values
   from the Application. Resources applied by hand can be dumped with
   [dumpall](https://github.com/guettli/dumpall), per namespace that contains
   only such resources:

   ```terminal
   dumpall -n legacy --skip-owned --ignore-config-use-common -o k8s/plain
   ```

3. Write `render.sh` (see above) with the same chart versions, values,
   release names and namespaces, and run it.

4. Compare the rendered state with the cluster, and adapt values or manifests
   until the diff is empty:

   ```terminal
   dumpall --read-resource-names-from zz_rendered --clean-out-dir --ignore-config-use-common -o current
   kubenorm diff current zz_rendered
   ```

5. Find resources that exist in the cluster but not in `zz_rendered/`, for
   example a Namespace created by `helm install --create-namespace`. Dump the
   whole namespaces. Resources only on the left side are missing from the
   render: add them to `k8s/plain`, except the HelmRelease and its source,
   which step 7 deletes:

   ```terminal
   dumpall -n demo,legacy --skip-owned --clean-out-dir --ignore-config-use-common -o all
   kubenorm diff all zz_rendered
   ```

6. Commit `render.sh`, the values, `k8s/plain` and `zz_rendered/`, and add the
   CI check.

7. Let the GitOps tool apply `zz_rendered/`. Hand the resources over without
   deleting them:
   - Argo CD: change the source of the existing Application in place, from
     the chart to `path: zz_rendered/...` with `directory.recurse: true`. The
     resources keep their tracking, Argo CD updates them in place.
   - Flux: set `spec.suspend: true` on the HelmRelease first, then delete the
     HelmRelease and its HelmRepository or OCIRepository (Flux skips the Helm
     uninstall for a suspended HelmRelease). Then create a Kustomization for
     `zz_rendered/...`; it takes over the existing resources.
   - `helm install` or `kubectl apply`: create the Application or Kustomization
     for `zz_rendered/...`.

   Then remove the Helm release history, so that nobody runs `helm upgrade`
   on top. It only deletes Secrets, not the resources:

   ```terminal
   kubectl delete secret -n NS -l owner=helm,name=RELEASE
   ```

8. Run the diff of step 4 again: it stays empty.

The resources keep their UID. Argo CD (client-side apply) removes the defaults
that kubenorm dropped, and the api-server sets them again: the `generation` of
a Deployment goes up once, but the Pod template is unchanged, so no Pods are
restarted. Flux (server-side apply) leaves fields set by Helm, like the
`app.kubernetes.io/managed-by: Helm` label, on the objects; they are harmless.

## `kubenorm norm`

Reads a file, a directory (recursive, `*.yaml` / `*.yml`) or stdin (`-`).

```terminal
# helm output -> normalized tree, one file per resource
helm template my-release ./chart | kubenorm norm --clean-out-dir -o zz_rendered/my-release -

# normalize to stdout (one multi-document stream, sorted)
kubenorm norm ./manifests

# CI guard: exit 1 and show a diff if hand-written manifests are not normalized
kubenorm norm ./k8s/plain --check
```

`--check` ignores comments, key order, file layout and empty
`labels`/`annotations` maps: it only fails if normalization would change the
content or drop a resource (for example a Pod, which the common config
skips). Resources outside `--namespaces`, `--kind` or `--name-regex` are not
checked.

### Helm and kustomize

kubenorm reads stdin, so it fits behind every renderer:

```terminal
kustomize build overlays/prod | kubenorm norm --clean-out-dir -o zz_rendered/prod -
```

With Helm 3 it also works as a post-renderer (Helm 4 only accepts
post-renderer plugins, use the pipe there):

```terminal
helm template demo ./chart --post-renderer kubenorm --post-renderer-args norm --post-renderer-args -
```

Use it for `helm template` (rendering for review, diff or RMP), not for
`helm install` / `helm upgrade`: the common config drops resources like bare
Pods, which would then not be deployed.

### Namespaces of rendered charts

Some charts rely on `helm --namespace` and do not set `metadata.namespace`.
`--add-namespace-if-missing NS` sets it on every namespaced resource without a
namespace, `--overwrite-namespace NS` sets it on all namespaced resources.

```terminal
helm template liqo liqo/liqo --namespace liqo | kubenorm norm - --add-namespace-if-missing liqo -o out/
```

kubenorm has no api-server to ask whether a kind is cluster-scoped. It uses,
in this order:

1. CRDs contained in the input or given with `--crds` (`spec.scope`),
2. the `clusterScopedResources` section of your config,
3. a built-in list of Kubernetes kinds and the cluster-scoped CRDs of common
   projects (cert-manager, Gateway API, Cilium, Calico, Antrea, Kyverno,
   Gatekeeper incl. its constraints, Crossplane, Karpenter, Kueue, Argo, ...),
   taken from their CRD manifests.

Everything else is treated as namespaced. The namespace is set before
filtering, so `--namespaces` and skip rules see the new value.

References to namespaces inside objects move along:

- `subjects[].namespace` of ServiceAccounts in RoleBindings and ClusterRoleBindings,
- `webhooks[].clientConfig.service.namespace` of Mutating- and
  ValidatingWebhookConfigurations,
- `spec.service.namespace` of APIServices,
- `spec.conversion.webhook.clientConfig.service.namespace` of CRDs,
- the cert-manager annotations `cert-manager.io/inject-ca-from` and
  `inject-ca-from-secret` (`namespace/name`).

`--overwrite-namespace` rewrites a reference if it points to a namespace of a
resource in the input (these resources move, so the reference has to move
too). References to other namespaces, like `kube-system`, stay.
`--add-namespace-if-missing` only fills empty references. An empty ServiceAccount
namespace in a RoleBinding means the namespace of the RoleBinding and stays
empty. `kubenorm explain` shows every change.

kubenorm warns on stderr if a `Namespace` object of the input is left behind by
`--overwrite-namespace`, and if a reference points to `default` although no
resource of the input is in `default`: usually the chart was rendered without
`helm template --namespace`, which charts use for references.

Compared to the namespace transformer of kustomize: kubenorm knows cluster-scoped
CRDs (kustomize sets a namespace on a `ClusterIssuer`), does not rename
`Namespace` objects, rewrites webhook service references, and only moves
references that point to moved namespaces (kustomize: subjects named `default`).

### Labels and annotations

`--set-label key=value` and `--set-annotation key=value` (repeatable) set a
label or annotation in `metadata` of every resource, replacing an existing
value. Pod templates and selectors are not changed. The values are set after
the remove rules, so a rule does not remove them. Keys and label values are
checked like the api-server does (not the total size of annotations).

To tell the reader of a resource, also in the cluster, which script
rendered it, use `--rendered-by`. It sets the annotation
`kubenorm.guettli.github.io/rendered-by`, which takes any value, also a
path. `kubenorm diff` shows it under "managed by" for resources on one side
only.

```terminal
helm template ./chart | kubenorm norm - --rendered-by foo/render.sh -o zz_rendered
```

Use it instead of `app.kubernetes.io/managed-by`: label values can't hold a
path, and that label names the tool that manages the resource in the
cluster (Helm adopts resources only if it says `Helm`).

`norm --check` with these flags fails until the files carry the values.
`diff` sets them on both sides, so differences in these keys are hidden.

```terminal
helm template ./chart | kubenorm norm - --set-label team=platform -o zz_rendered
```

### Secrets

Secret values are kept as they are (the files are your own input). Use
`--redact-secrets` to replace them with a marker, or `--secrets-as-stringdata`
to decode `data` into readable `stringData`. Values that are not printable
text (binary data, control characters other than newline, tab and carriage
return, or invisible characters like a non-breaking space, zero-width joiner or
BOM) stay base64-encoded in `data`. Existing `stringData` is folded in first,
so the result does not depend on how the input split the keys.

SOPS-encrypted documents (a top-level `sops` map with a `mac`) are skipped
with a note on stderr: re-encoding would reorder keys and break the SOPS MAC.

### pre-commit / prek

Use the kubenorm of your `mise.toml`, so the hook, `render.sh` and CI run the
same version:

```yaml
repos:
  - repo: local
    hooks:
      - id: kubenorm-check
        name: kubenorm norm --check
        entry: mise exec -- kubenorm norm --check
        language: system
        types: [yaml]
        files: ^k8s/plain/
```

The hook only sees the changed files. YAML files without `metadata`
(kustomization.yaml, CI configs) are ignored, but restrict the hook with
`files:` anyway: helm templates are not valid YAML.

Without mise, use the hook of this repository (`repo:
https://github.com/guettli/kubenorm`, `id: kubenorm-check`). It builds
kubenorm with Go; `prek update` (`pre-commit autoupdate`) sets `rev:` to the
latest release.

## `kubenorm diff`

Normalizes both sides with the same options and prints a unified diff.
Resources are paired by identity, not by file name, so differently organized
trees, single files and streams can be compared. Each side may contain a
resource only once: compare one cluster at a time. The `---`/`+++` headers
name the files a changed resource was read from (`<file>/<path>` for stdin or
a file with several resources).

A resource that exists on one side only is reported with one line instead
of its whole content. It names the file the resource was read from (plus
its path if the file holds several resources) and the tools that seem to
manage it, read from labels, annotations, ownerReferences and managedFields
before normalization removes them:

```text
Only here (old): current/demo/apps_Deployment/web.yaml managed by: helm release demo/web, manager helm
Only here (new): stdin (demo/ConfigMap/extra.yaml)
```

The side (`old` or `new`) is the argument the resource was found in.

Known tools: Helm, Argo CD, Flux, Fleet and other Wrangler objectsets,
Rancher, kapp, `kubectl apply`, `--rendered-by`, Config Sync, kpt/cli-utils
inventories, OLM, cert-manager, Kyverno generate rules, Crossplane, Gardener,
the Kubernetes addon-manager, Kluctl, and the copies of Reflector, kubed
(config-syncer) and kubernetes-replicator. A copy is named first: it carries
the labels and annotations of its source, so the hints after it may describe
the source. Add your own tools with `ownerHints` (see [Config](#config)).

Use `--full` to see the content as a diff from or to `/dev/null`. It labels
every resource with `a/<normalized path>` and `b/<normalized path>`, like
git, in the format of `git apply` and `patch -p1`. The header comment of a
file at the place where `norm -o` writes it is part of the diff, so the
patch applies to a tree written by `norm -o`. Apply it in the old
directory:

```terminal
kubenorm diff --full ../main/zz_rendered zz_rendered > changes.diff
patch -p1 -d ../main/zz_rendered < changes.diff   # or: cd ../main/zz_rendered && git apply ...
```

Whether a resource differs is decided without the header, so the files of
unchanged resources keep their old header (for example an older kubenorm
version).

```terminal
# What does this PR really change?
git worktree add ../main main
kubenorm diff ../main/zz_rendered zz_rendered

# Compare a cluster dump (from dumpall) with the desired state
dumpall --read-resource-names-from zz_rendered --clean-out-dir --ignore-config-use-common -o current
kubenorm diff current zz_rendered
```

`--output json` writes the differences for scripts:
`{"version": 1, "resources": [...]}` with, per resource, its normalized
`path`, `status` (`added`, `deleted`, `modified`), `old` and `new` (left out
for the side without the resource) with the `file` (`-` for stdin) and
`managedBy`, and the unified `diff` (for added and deleted resources only
with `--full`; labeled like the text output, with `--full` including the
file header comments). The text
format may change between releases; the JSON fields stay within a
`version`. The values of `managedBy` are for humans and may change.

`--output stat` writes a summary, like `git diff --stat`: one line per
differing resource with its status (`A`dded, `D`eleted, `M`odified), the
normalized path and the number of changed lines, then a total:

```text
 A demo/ConfigMap/added.yaml     | 9 +++++++++
 D demo/ConfigMap/legacy.yaml    | 7 -------
 M demo/apps_Deployment/web.yaml | 4 ++--
 3 resources changed (1 added, 1 deleted, 1 modified), 11 insertions(+), 9 deletions(-)
```

The files and managers of the resources are in the text and JSON output.
Like the text format, the stat format may change between releases.

For colors, write both sides with `norm -o` and use git:
`git diff --no-index old/ new/`.

Exit codes: `0` no differences, `1` differences, `2` error.

## `kubenorm explain`

Shows, per resource, every change and the rule that made it, or why the
resource is dropped. Useful when writing your own config: to turn a path of
the output into a field path, drop list indexes and write quoted keys with
escaped dots (`containers[0].image` -> `containers.image`,
`labels["helm.sh/chart"]` -> `labels.helm\.sh/chart`).

```text
$ kubenorm explain deploy.yaml
demo/apps_Deployment/web.yaml (from deploy.yaml)
  ~ spec.template.spec.containers[0].resources.limits.cpu: "1000m" -> "1"  [quantityFields[4] in common config: ...*ontainers.resources.limits.*]
  - metadata.uid: "1234"  [removeFields[1] in common config: metadata.uid]
  - spec.progressDeadlineSeconds: 600  [removeFields[19] in common config: spec.progressDeadlineSeconds]
demo/Pod/p.yaml (from deploy.yaml)
  dropped: skipResources[3] in common config
```

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
      - path: spec.completions     # only remove if parallelism is not set
        value: 1
        ifSiblingAbsent: parallelism  # also: ifSiblingPresent
      - path: spec.clusterIP     # remove unless the value is None
        exceptValue: None

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

quantityFields:
  - group: example.com
    kind: Database
    fields:
      - spec.storage.size
      - ...resources.limits.*

setMetadata:                   # set labels/annotations, after removeFields
  - kind: Deployment
    namespace: prod-*
    labels:
      team: platform
    annotations:
      example.com/owner: platform-team

ownerHints:                    # extra "managed by" hints of kubenorm diff
  - label: example.com/team
    text: team {value}         # default: "<key> {value}"
  - annotation: example.com/tool
    value: pulumi              # only if the value matches
    text: pulumi

keepFields:                    # never removed by removeFields
  - kind: Deployment
    fields:
      - metadata.annotations.configmanagement\.gke\.io/token
```

- Matchers (`group`, `kind`, `namespace`, `name`) are globs; omitted means `*`.
  Resources without `metadata.namespace` (cluster-scoped kinds, but also
  namespaced kinds that miss it) match the namespace `_cluster`.
  `--namespaces` drops them (except selected Namespace objects).
- Field paths use dots; escape literal dots as `\.`. Segments may use `*` / `?`.
  A bare `*` matches every key, also keys containing `/`; other globs do not
  match `/`, so `helm*` does not match `helm.sh/chart`.
  `...` matches any depth (`...labels.helm\.sh/chart`). Lists are walked
  automatically (`webhooks.clientConfig.caBundle`).
- `excludeNamespaces` drops the Namespace object and everything inside.
- `quantityFields` lists fields holding quantities. They are canonicalized
  exactly like the api-server does (`resource.ParseQuantity(v).String()`).
  The common config covers Pod specs of all built-in workloads, PVCs, PVs,
  ResourceQuotas and LimitRanges. Custom resources are only touched if their
  CRD is part of the input or given with `--crds DIR` (fields of type
  `resource.Quantity`, recognized by `x-kubernetes-int-or-string` and the
  quantity pattern) or if you list the fields here: other fields may only look
  like quantities. The CRD-based detection also works with `--no-common-config`.
  With `kubenorm diff`, put the CRDs on both sides (or on none) or use
  `--crds`, which applies to both sides, else the quantities differ.
  `--crds` only reads CRDs and ignores everything else, so it can point at a
  whole rendered tree: `kubenorm norm --check ./plain --crds ./zz_rendered`.
- Defaults of CRD schemas: if the CRD of a custom resource is part of the
  input or given with `--crds`, fields that hold the `default` of the schema
  (of the resource's API version) are removed, like the built-in defaults in
  the common config: the api-server sets them when they are absent. A parent
  is compared after its children lost their defaults, so an object that holds
  only defaults ends up as `{}` (and goes, if that is its default, too). This
  also works with `--no-common-config`. Like quantities, give `kubenorm diff`
  the CRDs on both sides or with `--crds`.
- `setMetadata` sets labels and annotations in `metadata` of the matching
  resources (pod templates are not changed), after `removeFields`. If
  several rules set a key, the later one wins (common config, then the
  config files in order); `--set-label`, `--set-annotation` and
  `--rendered-by` are applied last and win. A key without a value is an
  error, write `''` for an empty value. Put the rule in `.kubenorm.yaml`
  instead of repeating flags in render scripts.
- `ownerHints` adds hints to the "managed by" list of `kubenorm diff` for
  resources that carry a label or annotation, for tools kubenorm does not
  know. `{value}` in `text` is the value of the key (shortened after 100
  characters). Hints are read from the input before normalization, so keys
  set by `setMetadata` or `--set-label` do not trigger them.
- `keepFields` protects fields from all `removeFields` rules, also from
  those of the common config and the defaults read from CRDs. Use it to
  keep a single field instead of disabling the whole common config, for
  example the commit of Config Sync in a cluster dump. Matchers and paths
  work like in `removeFields` (without `version`). A path keeps exactly
  the field it names: `spec.template.spec.containers` keeps the list, but
  not the defaults inside it, use `...containers.*` for that. If a rule
  removes a parent (`status`), the kept child stays in it
  (`status.observedGeneration`). `kubenorm explain` does not list kept
  fields. `--keep-field PATH` (repeatable) does the same for all resources
  without a config file. Other steps still apply: quantities are
  canonicalized, and `managedFields` need `--keep-managed-fields`.
- The config is parsed strictly; unknown keys are an error.

`--config FILE` (repeatable) adds your rules to the common config,
`--no-common-config` disables the embedded one.

A `.kubenorm.yaml` in the current directory or a parent directory (up to the
root of the git repository, or your home directory outside of a repository) is
loaded automatically, before the `--config` files. Disable this with
`--no-config-discovery`.

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
  explain            Show which rule changes or drops what
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

cfg, _ := normalize.LoadConfig(true, "my-rules.yaml") // common config + own rules
opts := &normalize.Options{Config: cfg, SkipOwned: true}
resources, _ := normalize.ReadPaths([]string{"./manifests"}, os.Stdin)
resources, _ = opts.Process(resources)
rendered, _ := normalize.Render(resources)
_ = rendered.WriteDir("out")
```

## Development

- `mise install` installs the tools of `mise.toml` (go, prek, golangci-lint,
  and helm for `scripts/render-golden-inputs.sh`) in the versions CI uses,
  `prek install` the git hook. `prek run --all-files` runs the same checks
  as CI.
- `go test ./...` runs unit tests, golden tests (`testdata/golden`, rendered
  helm charts) and the seeds of the fuzz test. Update golden files with
  `go test -run TestGolden -update`, inputs with `scripts/render-golden-inputs.sh`.
- `go test ./normalize -run '^$' -fuzz FuzzIdempotent` checks that
  normalizing twice changes nothing.
- `scripts/check-defaults.sh` sends the golden outputs to a real kube-apiserver
  (envtest, no container runtime) with `kubectl apply --dry-run=server` and
  fails if the api-server adds a default that the common config does not
  remove. CI runs it for every change.
- Releases follow [semver](https://semver.org). Every merge to main creates the
  next minor release, once CI passed for it. It may change the normalized output: re-render in the
  commit that updates kubenorm. Breaking changes of the CLI or the
  `normalize` package need a new major version (module path
  `github.com/guettli/kubenorm/v2`, tagged by hand).

## Why not kyaml?

kubenorm reads and writes YAML like kubectl (`sigs.k8s.io/yaml` and
apimachinery), not with [kyaml](https://pkg.go.dev/sigs.k8s.io/kustomize/kyaml)
from kustomize/kpt. The two libraries have different goals:

- kyaml edits files that humans maintain. It keeps comments, key order and
  style, so that a tool changes as little as possible. Its formatter
  (`kio/filters.FormatFilter`) only reorders keys, and does not remove defaults,
  server fields or tool noise.
- kubenorm brings every input into one canonical form: two inputs with the same
  meaning must become byte-identical.

Using kubectl's libraries gives:

- output byte-identical to `kubectl get -o yaml` (keys sorted, lists not indented),
- the same parsing as kubectl and the api-server: YAML 1.1 (`image: y` becomes
  `true`, as kubectl would send it), integers as int64,
- fewer dependencies, and a single object model (`map[string]any`).

The trade-off: comments are lost. This is fine for rendered manifests, but it
means kubenorm does not rewrite hand-written files in place: `norm --check`
reports, `norm -o` writes a new tree.

## Related

- [dumpall](https://github.com/guettli/dumpall) Dump all resources of a cluster into the same layout.
- [konmon](https://github.com/guettli/konmon) Kubernetes Condition Monitor: watch all resources, keep a history in SQLite, alert via Alertmanager.
- [korphan](https://github.com/guettli/korphan) Find orphaned Kubernetes resources.
