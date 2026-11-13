#!/usr/bin/env bash
# Checks the default rules of the common config against a real kube-apiserver:
# every testdata/golden/*/want.yaml (already normalized) is sent to the
# api-server with `kubectl apply --dry-run=server`. The api-server adds its
# defaults; after normalizing, the answer must be identical to the file.
# A difference means a default that the common config does not remove.
#
# Needs no container runtime: etcd and kube-apiserver come from envtest.
set -euo pipefail

k8s_version=${K8S_VERSION:-1.37.0}
repo=$(realpath "$(dirname "$0")/..")
work=$(mktemp -d)
etcd_pid= apiserver_pid=
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

served=$(kubectl api-versions | sed 's#/.*##' | sort -u)

failed=0
for want in "$repo"/testdata/golden/*/want.yaml; do
	name=$(basename "$(dirname "$want")")

	# Custom resources need their CRD: skip groups the api-server does not serve.
	# spec.replicas: 1 is a default the common config keeps on purpose: an
	# absent value means "not managed" (for example by an HPA).
	cat >"$work/check.yaml" <<-'YAML'
		removeFields:
		  - group: apps
		    fields: [{path: spec.replicas, value: 1}]
		skipResources:
	YAML
	grep -h '^apiVersion: .*/' "$want" | sed 's#apiVersion: ##; s#/.*##' | sort -u |
		comm -23 - <(echo "$served") | sed 's/.*/  - group: "&"/' >>"$work/check.yaml"
	"$work/kubenorm" norm --no-config-discovery --config "$work/check.yaml" "$want" >"$work/$name.yaml"

	for ns in $(grep -h '^  namespace: ' "$work/$name.yaml" | sort -u | awk '{print $2}'); do
		kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f - >/dev/null 2>&1
	done
	kubectl apply --dry-run=server -o yaml -f "$work/$name.yaml" >"$work/$name-server.yaml"
	if ! "$work/kubenorm" diff --no-config-discovery --config "$work/check.yaml" "$work/$name.yaml" "$work/$name-server.yaml"; then
		echo "FAIL $name: the api-server adds defaults that are not normalized away (diff above)"
		failed=1
	else
		echo "ok   $name ($(grep -c '^kind:' "$work/$name.yaml") resources)"
	fi
done
exit "$failed"
