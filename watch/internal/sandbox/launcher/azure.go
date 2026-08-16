package launcher

// azureCredsStageDir is where the host's Azure CLI auth files are staged
// read-only inside the container. entrypoint.sh's seed_azure_creds
// (sandbox/lib/seed-auth.sh) reads exactly this directory and seeds the set
// once into /home/dev/.azure — the two paths are a contract; changing one
// without the other silently stops Azure auth from reaching the sandbox.
const azureCredsStageDir = "/tmp/host-azure-creds"

// azureCredFiles are the files under the host's ~/.azure that carry auth
// state, and the ONLY ones ever staged into a container. Kept byte-identical
// to AZURE_CRED_FILES in sandbox/lib/seed-auth.sh, which consumes them on the
// other side of the boundary:
//
//   - azureProfile.json — the signed-in identity and its subscriptions
//   - msal_token_cache.json — that identity's access/refresh tokens
//   - service_principal_entries.json — service-principal secrets
//
// The rest of ~/.azure (telemetry, command caches, logs, `az config`
// defaults) is deliberately left on the host: it is not auth, it can run to
// hundreds of megabytes, and the container has no reason to see it.
var azureCredFiles = []string{
	"azureProfile.json",
	"msal_token_cache.json",
	"service_principal_entries.json",
}

// RepoAzureConfig reads <repoRoot>/.cenci/config.json's "sandbox.azure" key —
// the opt-in that makes /cenci:configure bake sandbox/fragments/azure.dockerfile
// into the repo image. Failure classification is repoSandboxBoolConfig's:
// absent file/key and an explicit false are off, anything malformed is a hard
// error rather than a silent off.
func RepoAzureConfig(repoRoot string) (bool, error) {
	return repoSandboxBoolConfig(repoRoot, "azure")
}

// ResolveAzure reports whether this launch stages the host's Azure CLI
// credentials. Unlike dind there is no flag pair to reconcile — the repo's
// `sandbox.azure` opt-in is the only input — and unlike the claude/gh/pencil
// credentials, which every sandbox gets, Azure credentials are staged ONLY
// into repos that asked for the Azure CLI. Cloud-subscription credentials
// have a blast radius well beyond the repo, so the mount plan follows
// sandbox/AGENTS.md's "as restrictive as possible" rule and keeps them out of
// every container that has no `az` to use them.
//
// Non-repo scope resolves to off without reading anything: `sandbox.azure`
// lives in a repo's own .cenci/config.json, so outside repo scope there is no
// opt-in to honour.
func ResolveAzure(scope Scope) (bool, error) {
	if scope.WorkspaceScope != "repo" {
		return false, nil
	}
	return RepoAzureConfig(scope.RepoRoot)
}
