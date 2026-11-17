package main

import (
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// managers returns the systems that seem to manage the raw (not normalized)
// object, as found in its labels, annotations, ownerReferences and
// managedFields. Normalization removes all of these, so call it before.
func managers(obj map[string]any) []string {
	u := unstructured.Unstructured{Object: obj}

	var found []string

	add := func(s string) {
		if !slices.Contains(found, s) {
			found = append(found, s)
		}
	}

	addToolLabels(add, u.GetLabels(), u.GetAnnotations())

	for _, ref := range u.GetOwnerReferences() {
		if ref.Controller != nil && *ref.Controller {
			add("owner " + ref.Kind + "/" + ref.Name)
		}
	}

	for _, mf := range u.GetManagedFields() {
		// Status updates come from controllers, not from who manages the
		// desired state.
		if mf.Manager != "" && mf.Subresource == "" {
			add("manager " + mf.Manager)
		}
	}

	return found
}

// addToolLabels adds the tools found in the tracking labels and annotations
// of helm, argocd, flux, wrangler/fleet, kapp and kubectl apply.
func addToolLabels(add func(string), labels, annotations map[string]string) {
	addPair := func(what, ns, name string) {
		switch {
		case name == "":
		case ns == "":
			add(what + " " + name)
		default:
			add(what + " " + ns + "/" + name)
		}
	}

	addPair("helm release", annotations["meta.helm.sh/release-namespace"], annotations["meta.helm.sh/release-name"])

	switch by := labels["app.kubernetes.io/managed-by"]; by {
	case "":
	case "Helm":
		if annotations["meta.helm.sh/release-name"] == "" {
			add("helm")
		}
	default:
		add("managed-by " + by)
	}

	argoApp := labels["argocd.argoproj.io/instance"]
	if argoApp == "" {
		// Annotation tracking: "<app>:<group>/<kind>:<namespace>/<name>".
		argoApp, _, _ = strings.Cut(annotations["argocd.argoproj.io/tracking-id"], ":")
	}

	addPair("argocd app", "", argoApp)
	addPair("flux kustomization", labels["kustomize.toolkit.fluxcd.io/namespace"], labels["kustomize.toolkit.fluxcd.io/name"])
	addPair("flux helmrelease", labels["helm.toolkit.fluxcd.io/namespace"], labels["helm.toolkit.fluxcd.io/name"])
	addWrangler(add, annotations)
	addPair("kapp app", "", labels["kapp.k14s.io/app"])

	if _, ok := annotations["kubectl.kubernetes.io/last-applied-configuration"]; ok {
		add("kubectl apply")
	}
}

// addWrangler adds the objectset of Wrangler's apply, which Fleet, Rancher
// and the k3s/rke2 deploy and helm controllers use. Only an objectset owned
// by a fleet.cattle.io kind is Fleet.
func addWrangler(add func(string), annotations map[string]string) {
	id, hasID := annotations["objectset.rio.cattle.io/id"]
	ownerGVK := annotations["objectset.rio.cattle.io/owner-gvk"]

	if !hasID && ownerGVK == "" {
		return
	}

	what := "wrangler objectset"
	if strings.Contains(ownerGVK, "fleet.cattle.io") {
		what = "fleet"
	}

	if id != "" {
		what += " " + id
	}

	if owner := annotations["objectset.rio.cattle.io/owner-name"]; owner != "" {
		what += " (owner " + owner + ")"
	}

	add(what)
}
