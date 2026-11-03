package incident

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func validConfig(t *testing.T) Config {
	t.Helper()
	c := testConfig(t)
	if err := os.MkdirAll(c.Resources[0].Dir, 0700); err != nil {
		t.Fatal(err)
	}
	c.Namespace = "contoso.servicebus.windows.net"
	c.Queue = "alerts"
	c.Agent = ContainerConfig{Runtime: "podman", Image: "tools:reviewed", APIKeyEnv: "ANTHROPIC_API_KEY", Network: "incident-internal", ProxyURL: "http://proxy:3128"}
	c.Resources[0].Workspace = "00000000-0000-0000-0000-000000000000"
	c.Resources[0].Query = "AppExceptions | take 10"
	c.Resources[0].Workflow = "Use cenci stages and configured project gates."
	c.Auth = &AuthConfig{Mode: AuthModeCertificate, TenantID: testTenantID, ClientID: testClientID, CertificatePath: "/etc/cenci/incident-worker.pem"}
	return c
}

// writeAuthConfig writes validConfig with its auth member replaced by the raw
// JSON given (or removed when raw is empty) and returns the file path.
func writeAuthConfig(t *testing.T, raw string) string {
	t.Helper()
	b, err := json.Marshal(validConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if raw == "" {
		delete(doc, "auth")
	} else {
		doc["auth"] = json.RawMessage(raw)
	}
	b, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	put(t, path, string(b))
	return path
}

func TestAuthConfigAcceptsOnlyExplicitModesWithTheirOwnFields(t *testing.T) {
	const secret = "s3cr3t-never-echoed"
	cases := []struct {
		name string
		auth string
		want string // error marker naming the offending auth field; empty means accepted
	}{
		{"certificate", `{"mode":"servicePrincipalCertificate","tenantId":"` + testTenantID + `","clientId":"` + testClientID + `","certificatePath":"/etc/cenci/w.pem"}`, ""},
		{"workload", `{"mode":"workloadIdentity","tenantId":"` + testTenantID + `","clientId":"` + testClientID + `","tokenFilePath":"/var/run/token"}`, ""},
		{"managed-system-assigned", `{"mode":"managedIdentity","tenantId":"` + testTenantID + `"}`, ""},
		{"managed-user-assigned", `{"mode":"managedIdentity","tenantId":"` + testTenantID + `","clientId":"` + testClientID + `"}`, ""},
		{"managed-pinned", `{"mode":"managedIdentity","tenantId":"` + testTenantID + `","principalObjectId":"` + testObjectID + `"}`, ""},
		{"managed-user-assigned-pinned", `{"mode":"managedIdentity","tenantId":"` + testTenantID + `","clientId":"` + testClientID + `","principalObjectId":"` + testObjectID + `"}`, ""},
		{"managed-pinned-non-uuid", `{"mode":"managedIdentity","tenantId":"` + testTenantID + `","principalObjectId":"` + secret + `"}`, "auth.principalObjectId must be a UUID"},
		{"certificate-with-principal-object-id", `{"mode":"servicePrincipalCertificate","tenantId":"` + testTenantID + `","clientId":"` + testClientID + `","certificatePath":"/etc/w.pem","principalObjectId":"` + secret + `"}`, "auth.principalObjectId is not used by mode servicePrincipalCertificate"},
		{"workload-with-principal-object-id", `{"mode":"workloadIdentity","tenantId":"` + testTenantID + `","clientId":"` + testClientID + `","tokenFilePath":"/var/run/token","principalObjectId":"` + secret + `"}`, "auth.principalObjectId is not used by mode workloadIdentity"},
		{"wrong-case-principal-object-id", `{"mode":"managedIdentity","tenantId":"` + testTenantID + `","PrincipalObjectId":"` + testObjectID + `"}`, "auth.PrincipalObjectId"},
		{"mistyped-principal-object-id", `{"mode":"managedIdentity","tenantId":"` + testTenantID + `","principalObjectId":7}`, "auth.principalObjectId must be a string"},
		{"secret-mode", `{"mode":"servicePrincipalSecret","tenantId":"` + testTenantID + `","clientId":"` + testClientID + `"}`, "auth.mode"},
		{"missing-mode", `{"tenantId":"` + testTenantID + `"}`, "auth.mode"},
		{"client-secret-field", `{"mode":"servicePrincipalCertificate","tenantId":"` + testTenantID + `","clientId":"` + testClientID + `","certificatePath":"/etc/w.pem","clientSecret":"` + secret + `"}`, "auth.clientSecret"},
		{"wrong-case-field", `{"mode":"managedIdentity","tenantId":"` + testTenantID + `","ClientId":"` + testClientID + `"}`, "auth.ClientId"},
		{"missing-tenant", `{"mode":"managedIdentity"}`, "auth.tenantId"},
		{"non-uuid-tenant", `{"mode":"managedIdentity","tenantId":"contoso.onmicrosoft.com"}`, "auth.tenantId"},
		{"non-uuid-client", `{"mode":"managedIdentity","tenantId":"` + testTenantID + `","clientId":"not-a-uuid"}`, "auth.clientId"},
		{"certificate-missing-client", `{"mode":"servicePrincipalCertificate","tenantId":"` + testTenantID + `","certificatePath":"/etc/w.pem"}`, "auth.clientId"},
		{"certificate-missing-path", `{"mode":"servicePrincipalCertificate","tenantId":"` + testTenantID + `","clientId":"` + testClientID + `"}`, "auth.certificatePath"},
		{"certificate-relative-path", `{"mode":"servicePrincipalCertificate","tenantId":"` + testTenantID + `","clientId":"` + testClientID + `","certificatePath":"w.pem"}`, "auth.certificatePath"},
		{"certificate-with-token-file", `{"mode":"servicePrincipalCertificate","tenantId":"` + testTenantID + `","clientId":"` + testClientID + `","certificatePath":"/etc/w.pem","tokenFilePath":"/var/run/token"}`, "auth.tokenFilePath"},
		{"workload-missing-client", `{"mode":"workloadIdentity","tenantId":"` + testTenantID + `","tokenFilePath":"/var/run/token"}`, "auth.clientId"},
		{"workload-missing-token-file", `{"mode":"workloadIdentity","tenantId":"` + testTenantID + `","clientId":"` + testClientID + `"}`, "auth.tokenFilePath"},
		{"workload-relative-token-file", `{"mode":"workloadIdentity","tenantId":"` + testTenantID + `","clientId":"` + testClientID + `","tokenFilePath":"token"}`, "auth.tokenFilePath"},
		{"workload-with-certificate", `{"mode":"workloadIdentity","tenantId":"` + testTenantID + `","clientId":"` + testClientID + `","tokenFilePath":"/var/run/token","certificatePath":"/etc/w.pem"}`, "auth.certificatePath"},
		{"managed-with-certificate", `{"mode":"managedIdentity","tenantId":"` + testTenantID + `","certificatePath":"/etc/w.pem"}`, "auth.certificatePath"},
		{"managed-with-token-file", `{"mode":"managedIdentity","tenantId":"` + testTenantID + `","tokenFilePath":"/var/run/token"}`, "auth.tokenFilePath"},
		{"not-an-object", `"servicePrincipalCertificate"`, "auth must be an object"},
		{"mistyped-field", `{"mode":"managedIdentity","tenantId":7}`, "auth.tenantId"},
		{"workload-missing-tenant", `{"mode":"workloadIdentity","clientId":"` + testClientID + `","tokenFilePath":"/var/run/token"}`, "auth.tenantId is required"},
		{"certificate-missing-tenant", `{"mode":"servicePrincipalCertificate","clientId":"` + testClientID + `","certificatePath":"/etc/w.pem"}`, "auth.tenantId is required"},
		// Keys outside the safe pattern are still named, in escaped and bounded form.
		{"unknown-field-with-space", `{"mode":"managedIdentity","tenantId":"` + testTenantID + `","client secret":"` + secret + `"}`, `unknown field auth."client secret"`},
		{"unknown-field-over-64-chars", `{"mode":"managedIdentity","tenantId":"` + testTenantID + `","` + strings.Repeat("k", 70) + `":"` + secret + `"}`, `unknown field auth."` + strings.Repeat("k", 64) + `"...`},
		{"unknown-field-with-control-character", `{"mode":"managedIdentity","tenantId":"` + testTenantID + `","bad\u0001key":"` + secret + `"}`, `unknown field auth."bad\x01key"`},
		{"unknown-field-with-newline", `{"mode":"managedIdentity","tenantId":"` + testTenantID + `","forged\nline":"` + secret + `"}`, `unknown field auth."forged\nline"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := LoadConfig(writeAuthConfig(t, tc.auth))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("valid auth rejected: %v", err)
				}
				if c.Auth == nil {
					t.Fatal("accepted config lost its auth section")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error naming %s, got %v", tc.want, err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error echoed a configured value: %v", err)
			}
			if strings.Contains(err.Error(), strings.Repeat("k", 65)) {
				t.Fatalf("error echoed an over-long field name untruncated: %v", err)
			}
			for _, r := range err.Error() {
				if r < 0x20 || r == 0x7f {
					t.Fatalf("error carries the raw control character %U: %q", r, err.Error())
				}
			}
		})
	}
}

func TestAuthConfigDecodesIntoTheSelectedFields(t *testing.T) {
	c, err := LoadConfig(writeAuthConfig(t, `{"mode":"workloadIdentity","tenantId":"`+testTenantID+`","clientId":"`+testClientID+`","tokenFilePath":"/var/run/token"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := AuthConfig{Mode: AuthModeWorkloadIdentity, TenantID: testTenantID, ClientID: testClientID, TokenFilePath: "/var/run/token"}
	if c.Auth == nil || *c.Auth != want {
		t.Fatalf("auth decoded as %+v, want %+v", c.Auth, want)
	}

	c, err = LoadConfig(writeAuthConfig(t, `{"mode":"managedIdentity","tenantId":"`+testTenantID+`","principalObjectId":"`+testObjectID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	want = AuthConfig{Mode: AuthModeManagedIdentity, TenantID: testTenantID, PrincipalObjectID: testObjectID}
	if c.Auth == nil || *c.Auth != want {
		t.Fatalf("pinned managed auth decoded as %+v, want %+v", c.Auth, want)
	}
}

func TestConfigWithoutAuthIsRefusedWithMigrationMessage(t *testing.T) {
	for name, raw := range map[string]string{"absent": "", "null": "null"} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(writeAuthConfig(t, raw))
			if err == nil {
				t.Fatal("config without an explicit worker identity accepted")
			}
			for _, want := range []string{"no explicit worker identity is configured", "Worker identity", "watch/docs/incident-worker.md", "cenci incident setup --config"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("missing %q in %v", want, err)
				}
			}
		})
	}
}

// writeSetupConfig writes validConfig, as an operator has it before running
// setup (not yet enabled, no auth section), with the given edits applied.
func writeSetupConfig(t *testing.T, edit func(*Config), rawAuth string) string {
	t.Helper()
	c := validConfig(t)
	c.Enabled = false
	c.Auth = nil
	if edit != nil {
		edit(&c)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "auth")
	if rawAuth != "" {
		doc["auth"] = json.RawMessage(rawAuth)
	}
	if b, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	put(t, path, string(b))
	return path
}

func TestSetupConfigLoadsWithoutAuthButEnforcesPolicy(t *testing.T) {
	const secret = "s3cr3t-never-echoed"
	t.Run("disabled-config-without-auth-loads", func(t *testing.T) {
		path := writeSetupConfig(t, nil, "")
		if _, err := LoadConfig(path); err == nil {
			t.Fatal("control: the run loader must still refuse a disabled config without auth")
		}
		c, err := LoadSetupConfig(path)
		if err != nil {
			t.Fatalf("setup refused a config that only lacks enabled and auth: %v", err)
		}
		if c.Auth != nil || c.Namespace != "contoso.servicebus.windows.net" || c.Queue != "alerts" || len(c.Resources) != 1 {
			t.Fatalf("setup config decoded as %+v", c)
		}
	})
	t.Run("null-auth-loads", func(t *testing.T) {
		c, err := LoadSetupConfig(writeSetupConfig(t, nil, "null"))
		if err != nil || c.Auth != nil {
			t.Fatalf("null auth: %+v %v", c.Auth, err)
		}
	})
	t.Run("present-auth-is-kept", func(t *testing.T) {
		c, err := LoadSetupConfig(writeSetupConfig(t, nil, `{"mode":"servicePrincipalCertificate","tenantId":"`+testTenantID+`","clientId":"`+testClientID+`","certificatePath":"/etc/cenci/w.pem"}`))
		want := AuthConfig{Mode: AuthModeCertificate, TenantID: testTenantID, ClientID: testClientID, CertificatePath: "/etc/cenci/w.pem"}
		if err != nil || c.Auth == nil || *c.Auth != want {
			t.Fatalf("auth decoded as %+v (%v), want %+v", c.Auth, err, want)
		}
	})
	t.Run("present-auth-still-decodes-strictly", func(t *testing.T) {
		_, err := LoadSetupConfig(writeSetupConfig(t, nil, `{"mode":"servicePrincipalCertificate","tenantId":"`+testTenantID+`","clientId":"`+testClientID+`","certificatePath":"/etc/w.pem","clientSecret":"`+secret+`"}`))
		if err == nil || !strings.Contains(err.Error(), "auth.clientSecret") || strings.Contains(err.Error(), secret) {
			t.Fatalf("want an error naming auth.clientSecret without its value, got %v", err)
		}
	})
	for name, edit := range map[string]func(*Config){
		"zero-concurrency":   func(c *Config) { c.Concurrency = 0 },
		"ambiguous-broker":   func(c *Config) { c.Topic = "topic" },
		"unsafe-namespace":   func(c *Config) { c.Namespace = "evil.test/path.servicebus.windows.net" },
		"missing-workflow":   func(c *Config) { c.Resources[0].Workflow = "" },
		"no-resources":       func(c *Config) { c.Resources = nil },
		"relative-state":     func(c *Config) { c.StateDir = "state" },
		"bad-workspace":      func(c *Config) { c.Resources[0].Workspace = "not-a-uuid" },
		"wrong-runtime":      func(c *Config) { c.Agent.Runtime = "sh" },
		"path-traversal":     func(c *Config) { c.Resources[0].Runbooks = []string{"../secret"} },
		"duplicate-resource": func(c *Config) { c.Resources = append(c.Resources, c.Resources[0]) },
	} {
		t.Run("policy-"+name, func(t *testing.T) {
			if _, err := LoadSetupConfig(writeSetupConfig(t, edit, "")); err == nil {
				t.Fatal("a config that breaks the worker policy was accepted for setup")
			}
		})
	}
	t.Run("unknown-top-level-field", func(t *testing.T) {
		path := writeSetupConfig(t, nil, "")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		put(t, path, strings.Replace(string(b), `{`, `{"surprise":1,`, 1))
		if _, err := LoadSetupConfig(path); err == nil {
			t.Fatal("unknown config field accepted")
		}
	})
	t.Run("trailing-value", func(t *testing.T) {
		path := writeSetupConfig(t, nil, "")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		put(t, path, string(b)+`{}`)
		if _, err := LoadSetupConfig(path); err == nil {
			t.Fatal("trailing JSON value accepted")
		}
	})
}

func TestDisabledConfigReportsDisabledBeforeMissingAuth(t *testing.T) {
	c := validConfig(t)
	c.Enabled = false
	c.Auth = nil
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "disabled") || strings.Contains(err.Error(), "worker identity") {
		t.Fatalf("disabled check must come first: %v", err)
	}
}

func TestConfigRequiresExplicitOptInAndCompleteTrustedPolicy(t *testing.T) {
	for _, kind := range []string{"valid", "disabled", "zero-concurrency", "zero-budget", "rounded-zero-budget", "wrong-runtime", "production-key", "unsafe-namespace", "ambiguous-broker", "missing-workflow", "missing-checks", "path-traversal", "overlapping-deployments"} {
		t.Run(kind, func(t *testing.T) {
			c := validConfig(t)
			switch kind {
			case "disabled":
				c.Enabled = false
			case "zero-concurrency":
				c.Concurrency = 0
			case "zero-budget":
				c.BudgetUSD = 0
			case "rounded-zero-budget":
				c.BudgetUSD = 0.0000001
			case "wrong-runtime":
				c.Agent.Runtime = "sh"
			case "production-key":
				c.Agent.APIKeyEnv = "AZURE_CLIENT_SECRET"
			case "unsafe-namespace":
				c.Namespace = "evil.test/path.servicebus.windows.net"
			case "ambiguous-broker":
				c.Topic = "topic"
			case "missing-workflow":
				c.Resources[0].Workflow = ""
			case "missing-checks":
				c.Resources[0].Checks = nil
			case "path-traversal":
				c.Resources[0].Runbooks = []string{"../secret"}
			case "overlapping-deployments":
				c.Resources[0].Deployments = append(c.Resources[0].Deployments, c.Resources[0].Deployments[0])
			}
			b, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.json")
			put(t, path, string(b))
			_, err = LoadConfig(path)
			if kind == "valid" && err != nil {
				t.Fatal(err)
			}
			if kind != "valid" && err == nil {
				t.Fatal("incomplete policy accepted")
			}
		})
	}
}

func TestDeploymentUsesFiredTimeAndHistoricalBoundary(t *testing.T) {
	c := testConfig(t)
	r := c.Resources[0]
	boundary := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	r.Deployments[0].Until = boundary
	r.Deployments = append(r.Deployments, Deployment{Commit: strings.Repeat("b", 40), From: boundary})
	if got := r.deployed(boundary.Add(-time.Nanosecond)); got != strings.Repeat("a", 40) {
		t.Fatal("historical alert attributed to latest release")
	}
	if got := r.deployed(boundary); got != strings.Repeat("b", 40) {
		t.Fatal("exclusive deployment boundary incorrect")
	}
	if got := r.deployed(boundary.Add(-2 * time.Hour)); got != "" {
		t.Fatal("missing history guessed a release")
	}
}

func TestWorkerOwnershipAndCorruptLedgerFailClosed(t *testing.T) {
	c := testConfig(t)
	s := NewStore(c.StateDir)
	lock, err := s.WorkerLock()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	other, err := NewStore(c.StateDir).WorkerLock()
	if other != nil {
		_ = other.Close()
	}
	if err == nil {
		t.Fatal("second worker claimed same state directory")
	}
	put(t, filepath.Join(c.StateDir, "incidents.json"), `{"version":99,"incidents":{}}`)
	if _, err := s.List(); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("corrupt schema: %v", err)
	}
}

func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Ino
}

// withoutAuth re-marshals c with its auth section removed. Decoded configs are
// compared through JSON because reflect.DeepEqual trips on time.Time locations.
func withoutAuth(t *testing.T, c Config) string {
	t.Helper()
	c.Auth = nil
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func replacementAuth() AuthConfig {
	return AuthConfig{Mode: AuthModeCertificate, TenantID: testTenantID, ClientID: testClientID, CertificatePath: "/etc/cenci/new-worker.pem"}
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestWriteAuthChangesOnlyTheAuthSectionAtomicallyWithMode0600(t *testing.T) {
	for name, existing := range map[string]string{
		"no-auth-yet":    "",
		"replacing-auth": `{"mode":"managedIdentity","tenantId":"` + testTenantID + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := writeAuthConfig(t, existing)
			if err := os.Chmod(path, 0644); err != nil {
				t.Fatal(err)
			}
			before, err := decodeConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			inode := inodeOf(t, path)

			want := replacementAuth()
			if err := WriteAuth(path, want); err != nil {
				t.Fatal(err)
			}

			after, err := decodeConfig(path)
			if err != nil {
				t.Fatalf("the written config no longer decodes strictly: %v", err)
			}
			if after.Auth == nil || *after.Auth != want {
				t.Fatalf("auth %+v, want %+v", after.Auth, want)
			}
			if got, base := withoutAuth(t, after), withoutAuth(t, before); got != base {
				t.Fatalf("a field other than auth changed:\n before %s\n after  %s", base, got)
			}
			// Control: the comparison does see a change to another field.
			changed := before
			changed.Concurrency++
			if withoutAuth(t, changed) == withoutAuth(t, after) {
				t.Fatal("control failed, the comparison cannot see a changed non-auth field")
			}
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != 0600 {
				t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
			}
			if inodeOf(t, path) == inode {
				t.Fatal("the config was rewritten in place instead of replaced atomically")
			}
			if names := dirEntries(t, filepath.Dir(path)); len(names) != 1 || names[0] != "config.json" {
				t.Fatalf("temporary files were left behind: %v", names)
			}
		})
	}
}

func TestWriteAuthRefusesASymlinkOrAConfigOwnedByAnotherUser(t *testing.T) {
	uid := os.Getuid()
	read := func(t *testing.T, path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	t.Run("symlink", func(t *testing.T) {
		real := writeAuthConfig(t, "")
		link := filepath.Join(t.TempDir(), "config.json")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		original := read(t, real)
		if err := WriteAuth(link, replacementAuth()); err == nil {
			t.Fatal("WriteAuth wrote through a symbolic link")
		}
		if read(t, real) != original {
			t.Fatal("the symlink target was changed")
		}
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the symlink was replaced: %v, %v", fi, err)
		}
		if names := dirEntries(t, filepath.Dir(link)); len(names) != 1 {
			t.Fatalf("temporary files were left behind: %v", names)
		}
		// Control: the same content written to the real path succeeds.
		if err := WriteAuth(real, replacementAuth()); err != nil {
			t.Fatalf("control failed, WriteAuth refused a plain config: %v", err)
		}
	})
	t.Run("not-a-regular-file", func(t *testing.T) {
		dir := t.TempDir()
		if err := WriteAuth(dir, replacementAuth()); err == nil {
			t.Fatal("WriteAuth accepted a directory")
		}
	})
	t.Run("another-owner", func(t *testing.T) {
		path := writeAuthConfig(t, "")
		original := read(t, path)
		if err := writeAuth(path, replacementAuth(), uid+1); err == nil {
			t.Fatal("writeAuth accepted a config owned by another user")
		}
		if read(t, path) != original {
			t.Fatal("a foreign-owned config was changed")
		}
		// Control: the invoking uid is accepted for the same file.
		if err := writeAuth(path, replacementAuth(), uid); err != nil {
			t.Fatalf("control failed, writeAuth refused the owner's config: %v", err)
		}
	})
	t.Run("invalid-auth-and-unknown-fields-leave-the-file-alone", func(t *testing.T) {
		path := writeAuthConfig(t, "")
		original := read(t, path)
		if err := WriteAuth(path, AuthConfig{}); err == nil {
			t.Fatal("WriteAuth accepted an invalid auth section")
		}
		if read(t, path) != original {
			t.Fatal("an invalid auth section changed the file")
		}
		bad := filepath.Join(t.TempDir(), "config.json")
		put(t, bad, `{"enabled":true,"unknownField":1}`)
		if err := WriteAuth(bad, replacementAuth()); err == nil {
			t.Fatal("WriteAuth accepted a config with an unknown field")
		}
		if read(t, bad) != `{"enabled":true,"unknownField":1}` {
			t.Fatal("an undecodable config was changed")
		}
	})
}
