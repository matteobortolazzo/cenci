package launcher

import (
	"path/filepath"
	"strings"
	"testing"
)

// writeAzureCreds writes a full host-shaped ~/.azure auth set under home,
// plus the non-auth clutter (`az` telemetry and command caches) that shares
// that directory and must never be staged.
func writeAzureCreds(t *testing.T, home string) {
	t.Helper()
	writeFile(t, filepath.Join(home, ".azure", "azureProfile.json"), `{"subscriptions":[{"id":"sub"}]}`)
	writeFile(t, filepath.Join(home, ".azure", "msal_token_cache.json"), `{"RefreshToken":{}}`)
	writeFile(t, filepath.Join(home, ".azure", "service_principal_entries.json"), `[]`)
	writeFile(t, filepath.Join(home, ".azure", "telemetry.txt"), "telemetry")
	writeFile(t, filepath.Join(home, ".azure", "commands", "2026-01-01.log"), "log")
}

// writeSandboxConfig writes a .cenci/config.json into repo.
func writeSandboxConfig(t *testing.T, repo, body string) {
	t.Helper()
	writeFile(t, filepath.Join(repo, ".cenci", "config.json"), body)
}

// TestDryRun_CreateArgv_StagesAzureCredsOnlyWhenRepoOptsIn is the core #1080
// gate: Azure credentials reach a cloud subscription, so they are staged only
// into a repo that opted into the Azure CLI via `sandbox.azure`. A host with
// a full ~/.azure and a repo that never asked must produce no azure mount at
// all.
func TestDryRun_CreateArgv_StagesAzureCredsOnlyWhenRepoOptsIn(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	setFakeDockerNotRunning(t)
	writeAzureCreds(t, home)

	plan, err := dryRunEngine().DryRun(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("DryRun (no sandbox.azure): %v", err)
	}
	if joined := strings.Join(plan.CreateArgv, " "); strings.Contains(joined, "host-azure-creds") {
		t.Errorf("CreateArgv stages Azure credentials into a repo that never opted in:\n%s", joined)
	}

	writeSandboxConfig(t, repo, `{"sandbox":{"azure":true}}`)
	plan, err = dryRunEngine().DryRun(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("DryRun (sandbox.azure true): %v", err)
	}
	joined := strings.Join(plan.CreateArgv, " ")
	for _, name := range azureCredFiles {
		want := home + "/.azure/" + name + ":" + azureCredsStageDir + "/" + name + ":ro"
		if !strings.Contains(joined, want) {
			t.Errorf("CreateArgv missing the read-only Azure mount %q, got:\n%s", want, joined)
		}
	}
}

// TestDryRun_CreateArgv_StagesOnlyAzureAuthFiles pins the narrow staging set:
// ~/.azure also holds telemetry, command caches and logs that can run to
// hundreds of megabytes and are not auth — binding the whole directory would
// hand all of it to the container.
func TestDryRun_CreateArgv_StagesOnlyAzureAuthFiles(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	setFakeDockerNotRunning(t)
	writeAzureCreds(t, home)
	writeSandboxConfig(t, repo, `{"sandbox":{"azure":true}}`)

	plan, err := dryRunEngine().DryRun(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	joined := strings.Join(plan.CreateArgv, " ")
	for _, unwanted := range []string{"telemetry.txt", "commands"} {
		if strings.Contains(joined, "/.azure/"+unwanted) {
			t.Errorf("CreateArgv stages the non-auth ~/.azure entry %q:\n%s", unwanted, joined)
		}
	}
	if strings.Contains(joined, home+"/.azure:") {
		t.Errorf("CreateArgv binds the whole ~/.azure directory instead of the individual auth files:\n%s", joined)
	}
}

// TestDryRun_CreateArgv_SkipsAbsentAzureCredFiles covers the common partial
// case: a user-account login writes azureProfile.json and
// msal_token_cache.json but never service_principal_entries.json. An absent
// file must simply not be mounted — docker would otherwise create a
// directory at the host path and the container would see a directory where
// `az` expects a file.
func TestDryRun_CreateArgv_SkipsAbsentAzureCredFiles(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	setFakeDockerNotRunning(t)
	writeFile(t, filepath.Join(home, ".azure", "azureProfile.json"), `{"subscriptions":[]}`)
	writeSandboxConfig(t, repo, `{"sandbox":{"azure":true}}`)

	plan, err := dryRunEngine().DryRun(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	joined := strings.Join(plan.CreateArgv, " ")
	if !strings.Contains(joined, azureCredsStageDir+"/azureProfile.json:ro") {
		t.Errorf("CreateArgv missing the present azureProfile.json mount:\n%s", joined)
	}
	if strings.Contains(joined, "service_principal_entries.json") {
		t.Errorf("CreateArgv stages an absent Azure credential file:\n%s", joined)
	}
}

// TestDryRun_MalformedSandboxAzure_FailsClosed pins the #632 fail-closed
// rule for the new key: a wrong-typed "sandbox.azure" is a hard error, never
// a silent "no credentials staged" that would surface inside the sandbox as
// a mysteriously broken `az login`.
func TestDryRun_MalformedSandboxAzure_FailsClosed(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	setFakeDockerNotRunning(t)
	writeSandboxConfig(t, repo, `{"sandbox":{"azure":"yes"}}`)

	if _, err := dryRunEngine().DryRun(Options{Agent: "claude"}); err == nil {
		t.Fatal("DryRun succeeded with a non-boolean sandbox.azure; want a hard error")
	} else if !strings.Contains(err.Error(), "azure") {
		t.Errorf("error does not name the offending key: %v", err)
	}
}

// TestRepoAzureConfig_OffStates covers every input that must resolve to a
// safe off without an error: no config file, no sandbox object, no azure
// key, and an explicit false.
func TestRepoAzureConfig_OffStates(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string // "" means write no config file at all
	}{
		{"no config file", ""},
		{"no sandbox object", `{"configVersion":"1.0.0"}`},
		{"no azure key", `{"sandbox":{"dind":true}}`},
		{"explicit false", `{"sandbox":{"azure":false}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			if tc.body != "" {
				writeSandboxConfig(t, repo, tc.body)
			}
			on, err := RepoAzureConfig(repo)
			if err != nil {
				t.Fatalf("RepoAzureConfig: %v", err)
			}
			if on {
				t.Error("RepoAzureConfig = true, want false")
			}
		})
	}
}

// TestAudit_AzureCredentialSource_ProbedAlwaysStagedOnlyOnOptIn pins the
// audit view of #1080: the azure credential source is always reported with
// its resolved host path (an operator can see where cenci looks even in a
// repo that never opted in), Present tracks the host file, and Staged flips
// only once the repo turns `sandbox.azure` on.
func TestAudit_AzureCredentialSource_ProbedAlwaysStagedOnlyOnOptIn(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	eng := auditEngine()

	posture, err := eng.Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit (no azure creds): %v", err)
	}
	src := findCredentialSource(posture.CredentialSources, CredentialTypeAzure)
	if src == nil {
		t.Fatalf("expected an azure credentialSources entry, got %+v", posture.CredentialSources)
	}
	wantPath := filepath.Join(home, ".azure", "azureProfile.json")
	if src.HostPath != wantPath {
		t.Errorf("azure credential source HostPath = %q, want %q", src.HostPath, wantPath)
	}
	if src.Present || src.Staged {
		t.Errorf("azure credential source Present/Staged = %v/%v with no host creds, want false/false", src.Present, src.Staged)
	}

	// Host creds present, but the repo never opted in: visible to the probe,
	// deliberately NOT staged.
	writeAzureCreds(t, home)
	posture, err = eng.Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit (creds, no opt-in): %v", err)
	}
	src = findCredentialSource(posture.CredentialSources, CredentialTypeAzure)
	if src == nil || !src.Present {
		t.Fatalf("expected a present azure credentialSources entry, got %+v", posture.CredentialSources)
	}
	if src.Staged {
		t.Error("azure credential source Staged = true without the sandbox.azure opt-in, want false")
	}

	writeSandboxConfig(t, repo, `{"sandbox":{"azure":true}}`)
	posture, err = eng.Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit (creds + opt-in): %v", err)
	}
	src = findCredentialSource(posture.CredentialSources, CredentialTypeAzure)
	if src == nil || !src.Staged {
		t.Fatalf("expected a staged azure credentialSources entry after opt-in, got %+v", posture.CredentialSources)
	}
}

// TestAudit_AzureMounts_ClassifiedAndReadOnly pins that every staged Azure
// file classifies as MountKindAzureCreds — never the MountKindUnknown drift
// sentinel — and crosses the boundary read-only, per sandbox/AGENTS.md's
// bind-mount rule.
func TestAudit_AzureMounts_ClassifiedAndReadOnly(t *testing.T) {
	repo := newAuditTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(repo)
	writeAzureCreds(t, home)
	writeSandboxConfig(t, repo, `{"sandbox":{"azure":true}}`)

	posture, err := auditEngine().Audit(Options{Agent: "claude"})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}

	staged := 0
	for _, m := range posture.Mounts {
		if !strings.HasPrefix(m.Destination, azureCredsStageDir) {
			continue
		}
		staged++
		if m.Kind != MountKindAzureCreds {
			t.Errorf("Azure mount %q classified as %q, want %q", m.Destination, m.Kind, MountKindAzureCreds)
		}
		if !m.ReadOnly {
			t.Errorf("Azure credential mount %q must be read-only, got %+v", m.Destination, m)
		}
	}
	if staged != len(azureCredFiles) {
		t.Errorf("staged %d Azure mounts, want %d (%v)", staged, len(azureCredFiles), azureCredFiles)
	}
}

// TestResolveAzure_NonRepoScopeIsOff pins that a non-repo scope resolves off
// without reading anything — `sandbox.azure` lives in a repo's own config, so
// outside repo scope there is no opt-in to honour.
func TestResolveAzure_NonRepoScopeIsOff(t *testing.T) {
	repo := t.TempDir()
	writeSandboxConfig(t, repo, `{"sandbox":{"azure":true}}`)

	on, err := ResolveAzure(Scope{WorkspaceScope: "default", RepoRoot: repo})
	if err != nil {
		t.Fatalf("ResolveAzure: %v", err)
	}
	if on {
		t.Error("ResolveAzure = true outside repo scope, want false")
	}
}
