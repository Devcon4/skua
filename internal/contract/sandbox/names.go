// Package sandbox is the pod contract between the operator, skua-api and the runner: object names,
// labels, the run Secret layout, and the sandbox pod's layout and env.
package sandbox

// SecretName returns the name of the run Secret holding the named run's credentials.
func SecretName(run string) string {
	return "skua-run-" + run
}

// Name returns the name of the SandboxTemplate and SandboxWarmPool of the named profile.
func Name(profile string) string {
	return "skua-" + profile
}
