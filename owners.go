package main

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/guettli/kubenorm/normalize"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// managers returns the systems that seem to manage the raw (not normalized)
// object, as found in its labels, annotations, ownerReferences and
// managedFields, plus the owner hints of the config. Normalization removes
// all of these, so call it before.
func managers(obj map[string]any, cfg *normalize.Config) []string {
	u := unstructured.Unstructured{Object: obj}

	var found []string

	add := func(s string) {
		// Values come from the input: keep the output on one line.
		if strings.ContainsFunc(s, unicode.IsControl) {
			s = strconv.Quote(s)
		}

		if !slices.Contains(found, s) {
			found = append(found, s)
		}
	}

	labels, annotations := u.GetLabels(), u.GetAnnotations()
	// Copies carry the labels and annotations of their source: name the
	// copy first.
	addReplicators(add, labels, annotations)
	addToolLabels(add, labels, annotations)
	addPlatformTools(add, u.GetName(), labels, annotations)

	for _, hint := range cfg.OwnerHints(labels, annotations) {
		add(hint)
	}

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
// of helm, argocd, flux, wrangler/fleet, rancher, kapp and kubectl apply.
func addToolLabels(add func(string), labels, annotations map[string]string) {
	addPair := func(what, ns, name string) {
		if name != "" {
			add(what + " " + nsName(ns, name))
		}
	}

	addPair("helm release", annotations["meta.helm.sh/release-namespace"], annotations["meta.helm.sh/release-name"])

	switch by := labels["app.kubernetes.io/managed-by"]; by {
	case "":
	case "Helm":
		if annotations["meta.helm.sh/release-name"] == "" {
			add("helm")
		}
	case "configmanagement.gke.io":
		if annotations["configmanagement.gke.io/managed"] != "enabled" {
			add("managed-by " + by)
		}
	case "kyverno":
		if labels["generate.kyverno.io/policy-name"] == "" {
			add("managed-by " + by)
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
	addRancher(add, labels, annotations)
	addPair("kapp app", "", labels["kapp.k14s.io/app"])

	addPair("rendered by", "", annotations[normalize.RenderedByAnnotation])

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

// rancherOwnerValues are label values of Rancher whose key names the owner
// (a "<namespace>_<name>" of a role template binding, or a UID).
var rancherOwnerValues = []string{
	"membership-binding-owner",
	"memberhsip-binding-owner", // Rancher's legacy misspelling
	"prtb-in-cluster-binding-owner",
	"crtb-in-project-binding-owner",
	"owner-user",
}

// rancherOwnerLabels are Rancher labels whose value names the owner.
var rancherOwnerLabels = []struct{ key, what string }{
	// "<namespace>_<name>" of the PRTB or CRTB, see rancherRTB.
	{"authz.cluster.cattle.io/rtb-owner-updated", "rtb"},
	{"authz.cluster.cattle.io/crtb-owner", "crtb"},
	{"authz.cluster.cattle.io/clusterrole-owner", "roletemplate"},
	{"authz.management.cattle.io/gr-owner", "globalrole"},
	{"authz.management.cattle.io/grb-owner", "globalrolebinding"},
}

// addRancher adds the Rancher objects that own RBAC resources (role
// template bindings, role templates, global roles) and the creator of
// objects made through the Norman API.
func addRancher(add func(string), labels, annotations map[string]string) {
	for _, l := range rancherOwnerLabels {
		if v := labels[l.key]; v != "" {
			add("rancher " + l.what + " " + rancherRTB(v))
		}
	}

	// The legacy label holds the UID of the binding.
	if uid := labels["authz.cluster.cattle.io/rtb-owner"]; uid != "" && labels["authz.cluster.cattle.io/rtb-owner-updated"] == "" {
		add("rancher rtb uid " + uid)
	}

	keys := slices.Sorted(maps.Keys(labels))

	for _, k := range keys {
		v := labels[k]

		for _, prefix := range []string{"prtb", "crtb"} {
			if name, ok := strings.CutPrefix(k, "authz.cluster.cattle.io/"+prefix+"-owner-"); ok && v == "true" {
				add("rancher " + prefix + " " + rancherRTB(name))
			}
		}

		if slices.Contains(rancherOwnerValues, v) {
			add("rancher " + v + " " + rancherRTB(k))
		}
	}

	if labels["cattle.io/creator"] == "norman" {
		norman := "rancher norman"
		if user := annotations["field.cattle.io/creatorId"]; user != "" {
			norman += " (creator " + user + ")"
		}

		add(norman)
	}
}

// rancherRTB turns "<namespace>_<name>" into "<namespace>/<name>". Rancher
// shortens values longer than 63 characters to a prefix plus a hash, so such
// a name may not exist.
func rancherRTB(v string) string {
	out := v
	if ns, name, ok := strings.Cut(v, "_"); ok {
		out = ns + "/" + name
	}

	if len(v) == 63 {
		out += " (truncated)"
	}

	return out
}
