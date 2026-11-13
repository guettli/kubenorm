#!/usr/bin/env bash
# Renders the helm charts used as input of the golden tests (testdata/golden).
# Update the versions here, run the script, then: go test -run TestGolden -update
set -euo pipefail
golden=$(realpath "$(dirname "$0")/../testdata/golden")
cd "$(mktemp -d)" # helm prefers a local directory named like the chart

render() { # case release chart repo version helm-args...
	local dir=$1 release=$2 chart=$3 repo=$4 version=$5
	shift 5
	mkdir -p "$golden/$dir"
	helm template "$release" "$chart" --repo "$repo" --version "$version" "$@" >"$golden/$dir/input.yaml"
}

render cert-manager cert-manager cert-manager https://charts.jetstack.io v1.21.3 --namespace cert-manager
echo "" >"$golden/cert-manager/args"

# --overwrite-namespace moves resources and references (webhook, RBAC subjects).
render ingress-nginx ingress-nginx ingress-nginx https://kubernetes.github.io/ingress-nginx 4.15.1 --namespace ingress-nginx
echo "--overwrite-namespace ingress" >"$golden/ingress-nginx/args"

# liqo leaves metadata.namespace empty on some resources.
render liqo liqo liqo https://helm.liqo.io v1.3.0 --namespace liqo
echo "--add-namespace-if-missing liqo" >"$golden/liqo/args"
