package main

// addPlatformTools adds the controllers of cluster platforms and operators
// that create resources: Config Sync, kpt/cli-utils inventories, OLM,
// cert-manager, Kyverno, Crossplane, Gardener, the Kubernetes addon-manager
// and Kluctl. name is the name of the object.
func addPlatformTools(add func(string), name string, labels, annotations map[string]string) {
	// Config Sync applies with cli-utils: its hint says more than the
	// inventory.
	if !addConfigSync(add, annotations) {
		if inv := annotations["config.k8s.io/owning-inventory"]; inv != "" {
			add("kpt/cli-utils inventory " + inv)
		}
	}

	if owner := labels["olm.owner"]; owner != "" {
		what := "olm"
		if kind := labels["olm.owner.kind"]; kind != "" {
			what += " " + kind
		}

		add(what + " " + nsName(labels["olm.owner.namespace"], owner))
	}

	if cert := annotations["cert-manager.io/certificate-name"]; cert != "" {
		add("cert-manager certificate " + cert)
	}

	if policy := labels["generate.kyverno.io/policy-name"]; policy != "" {
		add("kyverno policy " + nsName(labels["generate.kyverno.io/policy-namespace"], policy))
	}

	switch {
	case labels["crossplane.io/claim-name"] != "":
		add("crossplane claim " + nsName(labels["crossplane.io/claim-namespace"], labels["crossplane.io/claim-name"]))
	// A composite resource carries its own name.
	case labels["crossplane.io/composite"] != "" && labels["crossplane.io/composite"] != name:
		add("crossplane composite " + labels["crossplane.io/composite"])
	}

	if origin := annotations["resources.gardener.cloud/origin"]; origin != "" {
		add("gardener managedresource " + origin)
	}

	if mode := labels["addonmanager.kubernetes.io/mode"]; mode != "" {
		add("addon-manager " + mode)
	}

	addKluctl(add, labels, annotations)
}

// addConfigSync adds Config Sync (Anthos Config Management): the RootSync or
// RepoSync and the file in the repository. It reports whether it found it.
func addConfigSync(add func(string), annotations map[string]string) bool {
	if annotations["configmanagement.gke.io/managed"] != "enabled" {
		return false
	}

	what := "config sync"
	if manager := annotations["configsync.gke.io/manager"]; manager != "" {
		what += " " + manager
	}

	if path := annotations["configmanagement.gke.io/source-path"]; path != "" {
		what += " (file " + path + ")"
	}

	add(what)

	return true
}

// addKluctl adds Kluctl, which labels deployed objects with the
// discriminator of the target and annotates the deployment item directory.
func addKluctl(add func(string), labels, annotations map[string]string) {
	disc, dir := labels["kluctl.io/discriminator"], annotations["kluctl.io/deployment-item-dir"]
	if disc == "" && dir == "" {
		return
	}

	what := "kluctl"
	if disc != "" {
		what += " " + disc
	}

	if dir != "" {
		what += " (dir " + dir + ")"
	}

	add(what)
}

// addReplicators adds copies made by Reflector, kubed/config-syncer and the
// mittwald kubernetes-replicator, with the source object. The copies carry
// the labels and annotations of the source, so the other hints may describe
// the source.
func addReplicators(add func(string), labels, annotations map[string]string) {
	if src := annotations["reflector.v1.k8s.emberstack.com/reflects"]; src != "" {
		add("reflector copy of " + src)
	}

	if name := labels["kubed.appscode.com/origin.name"]; name != "" {
		add("kubed copy of " + nsName(labels["kubed.appscode.com/origin.namespace"], name))
	}

	const mittwald = "replicator.v1.mittwald.de/"

	switch {
	case annotations[mittwald+"replicate-from"] != "":
		add("kubernetes-replicator copy of " + annotations[mittwald+"replicate-from"])
	case annotations[mittwald+"replicated-from-source"] != "":
		add("kubernetes-replicator copy of " + annotations[mittwald+"replicated-from-source"])
	case annotations[mittwald+"replicated-from-version"] != "":
		add("kubernetes-replicator copy")
	}
}

// nsName returns "<namespace>/<name>", or the name without a namespace.
func nsName(ns, name string) string {
	if ns == "" {
		return name
	}

	return ns + "/" + name
}
