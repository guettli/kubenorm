#!/usr/bin/env bash
# Checks the default rules of the common config and of CRD schemas against a
# real kube-apiserver: every testdata/golden/*/want.yaml (already normalized)
# is sent to the api-server with `kubectl apply --dry-run=server`, after the
# CRDs of the case are installed. The api-server adds its
# defaults (and drops some explicit zero values); after normalizing, the
# answer must be identical to the file. A difference means a rule is missing.
#
# Needs no container runtime: etcd and kube-apiserver come from envtest.
set -euo pipefail

k8s_version=${K8S_VERSION:-1.37.0}
repo=$(realpath "$(dirname "$0")/..")
work=$(mktemp -d)
etcd_pid='' apiserver_pid=''
cleanup() {
	# Stop the api-server before etcd, it waits for etcd while shutting down.
	[ -z "$apiserver_pid" ] || { kill "$apiserver_pid"; wait "$apiserver_pid"; } 2>/dev/null || true
	[ -z "$etcd_pid" ] || { kill "$etcd_pid"; wait "$etcd_pid"; } 2>/dev/null || true
	rm -rf "$work"
}
trap cleanup EXIT

bin=$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.25.2 use "$k8s_version" -p path)
go build -o "$work/kubenorm" "$repo"

openssl genrsa -out "$work/sa.key" 2048 2>/dev/null
echo "check-defaults-token,admin,admin,system:masters" >"$work/tokens.csv"

"$bin/etcd" --data-dir "$work/etcd" \
	--listen-client-urls http://127.0.0.1:23790 --advertise-client-urls http://127.0.0.1:23790 \
	--listen-peer-urls http://127.0.0.1:23800 >"$work/etcd.log" 2>&1 &
etcd_pid=$!

"$bin/kube-apiserver" --etcd-servers http://127.0.0.1:23790 \
	--bind-address 127.0.0.1 --secure-port 26443 --cert-dir "$work/certs" \
	--token-auth-file "$work/tokens.csv" --authorization-mode AlwaysAllow \
	--service-account-issuer https://kubernetes.default.svc \
	--service-account-key-file "$work/sa.key" --service-account-signing-key-file "$work/sa.key" \
	--service-cluster-ip-range 10.0.0.0/24 --allow-privileged >"$work/apiserver.log" 2>&1 &
apiserver_pid=$!

kubectl() {
	"$bin/kubectl" --server https://127.0.0.1:26443 --token check-defaults-token \
		--insecure-skip-tls-verify "$@"
}

for _ in $(seq 60); do
	kubectl get --raw /readyz >/dev/null 2>&1 && break
	sleep 1
done
kubectl get --raw /readyz >/dev/null || { tail -20 "$work/apiserver.log"; exit 1; }

failed=0
for want in "$repo"/testdata/golden/*/want.yaml; do
	name=$(basename "$(dirname "$want")")

	# Install the CRDs of the case, so its custom resources are checked, too.
	"$work/kubenorm" norm --no-config-discovery --kind CustomResourceDefinition "$want" >"$work/$name-crds.yaml"
	if grep -q '^kind:' "$work/$name-crds.yaml"; then
		kubectl apply -f "$work/$name-crds.yaml" >/dev/null
		kubectl wait --for condition=established --timeout=60s -f "$work/$name-crds.yaml" >/dev/null
	fi
	served=$(kubectl api-versions | sed 's#/.*##' | sort -u)

	# Custom resources need their CRD: skip groups the api-server does not serve.
	# spec.replicas: 1 is a default the common config keeps on purpose: an
	# absent value means "not managed" (for example by an HPA).
	cat >"$work/check.yaml" <<-'YAML'
		removeFields:
		  - group: apps
		    fields: [{path: spec.replicas, value: 1}]
		skipResources:
	YAML
	{ grep -h '^apiVersion: .*/' "$want" || true; } | sed 's#apiVersion: ##; s#/.*##' | sort -u |
		comm -23 - <(echo "$served") | sed 's/.*/  - group: "&"/' >>"$work/check.yaml"
	"$work/kubenorm" norm --no-config-discovery --config "$work/check.yaml" "$want" >"$work/$name.yaml"

	for ns in $(grep -h '^  namespace: ' "$work/$name.yaml" | sort -u | awk '{print $2}'); do
		kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f - >/dev/null 2>&1
	done
	kubectl apply --dry-run=server -o yaml -f "$work/$name.yaml" >"$work/$name-server.yaml"
	if ! "$work/kubenorm" diff --no-config-discovery --config "$work/check.yaml" "$work/$name.yaml" "$work/$name-server.yaml"; then
		echo "FAIL $name: the api-server changes fields that normalizing does not cover (diff above)"
		failed=1
	else
		echo "ok   $name ($(grep -c '^kind:' "$work/$name.yaml") resources)"
	fi

	# Removing CRD schema defaults must not change the meaning: the api-server
	# answers the same for the custom resources of input.yaml and want.yaml.
	# Only for cases without args (the api-server takes their input as is).
	dir=$(dirname "$want")
	[ ! -s "$dir/args" ] && grep -q '^kind:' "$work/$name-crds.yaml" || continue
	for f in input want; do
		python3 - "$work/$name-crds.yaml" "$dir/$f.yaml" >"$work/$name-$f-crs.yaml" <<-'PY'
			import sys, yaml
			crds = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
			kinds = {(c["spec"]["group"], c["spec"]["names"]["kind"]) for c in crds}
			docs = [d for d in yaml.safe_load_all(open(sys.argv[2])) if d]
			yaml.safe_dump_all([d for d in docs if (d["apiVersion"].split("/")[0], d["kind"]) in kinds], sys.stdout)
		PY
	done
	grep -q '^kind:' "$work/$name-input-crs.yaml" || continue
	for f in input want; do
		kubectl apply --dry-run=server -o json -f "$work/$name-$f-crs.yaml" >"$work/$name-$f-crs.json"
	done
	if ! python3 - "$work/$name-input-crs.json" "$work/$name-want-crs.json" <<-'PY'; then
		import json, sys
		def objects(path):
		    doc = json.load(open(path))
		    out = {}
		    for o in doc.get("items", [doc]):
		        m = o.pop("metadata")
		        out[(o["kind"], m.get("namespace"), m["name"])] = o
		    return out
		a, b = objects(sys.argv[1]), objects(sys.argv[2])
		for key in sorted(a.keys() | b.keys()):
		    if a.get(key) != b.get(key):
		        print(key, "\n  input:", a.get(key), "\n  want: ", b.get(key))
		sys.exit(a != b)
	PY
		echo "FAIL $name: normalizing changed the meaning of custom resources (above)"
		failed=1
	else
		echo "ok   $name: custom resources mean the same as in input.yaml"
	fi
done
exit "$failed"
