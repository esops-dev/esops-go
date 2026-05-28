package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/pkg/config"
)

// readRecords reads the audit file and returns one parsed Record per
// line. Fails the test on any decode error so consumers don't need
// boilerplate per call site.
func readRecords(t *testing.T, path string) []Record {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test-local path under TempDir
	if err != nil {
		t.Fatalf("read audit file: %v", err)
	}
	var out []Record
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}

func TestInit_emptyPathDisablesAuditing(t *testing.T) {
	Reset()
	Init("")
	if Enabled() {
		t.Errorf("Init(\"\") left auditing enabled")
	}
}

func TestInit_opensFileAndWritesRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")

	Reset()
	Init(path)
	if !Enabled() {
		t.Fatal("Init(path): Enabled() returned false")
	}
	Begin("ops health", []string{"esops", "ops", "health"}, "local-es", "http://localhost:9200", false)
	Finish(0, "")

	records := readRecords(t, path)
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	r := records[0]
	if r.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %d, want %d", r.SchemaVersion, SchemaVersion)
	}
	if r.Command != "ops health" {
		t.Errorf("command = %q, want \"ops health\"", r.Command)
	}
	if r.Context != "local-es" {
		t.Errorf("context = %q, want local-es", r.Context)
	}
	if r.ClusterURL != "http://localhost:9200" {
		t.Errorf("cluster_url = %q, want http://localhost:9200", r.ClusterURL)
	}
	if r.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", r.ExitCode)
	}
	if r.Error != "" {
		t.Errorf("error = %q, want empty", r.Error)
	}
	if r.Timestamp == "" {
		t.Error("timestamp is empty")
	}
}

func TestInit_filePermissionsAre0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")

	Reset()
	Init(path)
	Begin("ops health", []string{"esops", "ops", "health"}, "local-es", "http://localhost:9200", false)
	Finish(0, "")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perms = %o, want 0600", perm)
	}
}

func TestInit_brokenPathDisablesWithoutPanic(t *testing.T) {
	// Parent directory missing: open fails, auditing must end up
	// disabled and no record should be attempted. Begin/Finish must
	// not panic.
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-dir", "audit.log")

	Reset()
	Init(missing)
	if Enabled() {
		t.Errorf("Init with unopenable path left auditing enabled")
	}
	Begin("ops health", []string{"esops", "ops", "health"}, "", "", false)
	Finish(0, "")
	// No assertion on file contents — there is no file.
}

func TestBegin_withoutInitIsNoop(t *testing.T) {
	Reset()
	Begin("ops health", []string{"esops", "ops", "health"}, "ctx", "http://x", false)
	Finish(1, "auth")
	// Nothing to assert except that nothing panicked and Enabled
	// remains false.
	if Enabled() {
		t.Error("Begin without Init should not enable auditing")
	}
}

func TestFinish_appendsAcrossInvocations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")

	for i, ev := range []struct{ cmd, ctx string }{
		{"ops health", "local-es"},
		{"index list", "local-os"},
	} {
		Reset()
		Init(path)
		Begin(ev.cmd, []string{"esops", ev.cmd}, ev.ctx, "http://localhost:9200", false)
		Finish(0, "")
		_ = i
	}

	records := readRecords(t, path)
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2 (audit log should append, not truncate)", len(records))
	}
	if records[0].Command != "ops health" || records[1].Command != "index list" {
		t.Errorf("commands = [%q, %q], want [\"ops health\", \"index list\"]",
			records[0].Command, records[1].Command)
	}
}

func TestFinish_recordsExitCodeAndErrorClass(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")

	Reset()
	Init(path)
	Begin("ops health", []string{"esops", "ops", "health"}, "local-es", "http://localhost:9200", false)
	Finish(4, "auth")

	records := readRecords(t, path)
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	if records[0].ExitCode != 4 {
		t.Errorf("exit_code = %d, want 4", records[0].ExitCode)
	}
	if records[0].Error != "auth" {
		t.Errorf("error = %q, want auth", records[0].Error)
	}
}

func TestSummary_attachesPayload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")

	Reset()
	Init(path)
	Begin("migrate reindex", []string{"esops", "migrate", "reindex"}, "local-es", "http://localhost:9200", false)
	Summary(map[string]any{"source": "idx-v1", "target": "idx-v2", "docs": 12345})
	Finish(0, "")

	records := readRecords(t, path)
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	got, ok := records[0].Summary.(map[string]any)
	if !ok {
		t.Fatalf("summary missing or wrong type: %#v", records[0].Summary)
	}
	if got["source"] != "idx-v1" || got["target"] != "idx-v2" {
		t.Errorf("summary fields wrong: %#v", got)
	}
}

func TestRedactArgs_passwordSpaceSeparated(t *testing.T) {
	in := []string{"esops", "--password", "hunter2", "ops", "health"}
	out := RedactArgs(in)
	for _, leak := range []string{"hunter2"} {
		for _, tok := range out {
			if strings.Contains(tok, leak) {
				t.Errorf("RedactArgs leaked %q in token %q", leak, tok)
			}
		}
	}
}

func TestRedactArgs_passwordEqualsForm(t *testing.T) {
	in := []string{"esops", "--password=hunter2", "ops", "health"}
	out := RedactArgs(in)
	for _, tok := range out {
		if strings.Contains(tok, "hunter2") {
			t.Errorf("RedactArgs leaked password in --flag=value form: %q", tok)
		}
	}
}

func TestRedactArgs_apiKeyAndToken(t *testing.T) {
	in := []string{"esops", "--api-key", "ak-1234", "--token", "bearer-xyz", "ops", "health"}
	out := RedactArgs(in)
	for _, leak := range []string{"ak-1234", "bearer-xyz"} {
		for _, tok := range out {
			if strings.Contains(tok, leak) {
				t.Errorf("RedactArgs leaked %q: out=%v", leak, out)
			}
		}
	}
}

func TestRedactArgs_nonSensitiveFlagsPassThrough(t *testing.T) {
	in := []string{"esops", "--context", "local-es", "--output", "json", "ops", "health"}
	out := RedactArgs(in)
	want := []string{"esops", "--context", "local-es", "--output", "json", "ops", "health"}
	if len(out) != len(want) {
		t.Fatalf("len(out) = %d, want %d", len(out), len(want))
	}
	for i := range out {
		if out[i] != want[i] {
			t.Errorf("out[%d] = %q, want %q", i, out[i], want[i])
		}
	}
}

func TestRedactArgs_authorizationLikeFlagRedacts(t *testing.T) {
	in := []string{"esops", "--authorization", "Bearer abc", "ops", "health"}
	out := RedactArgs(in)
	for _, tok := range out {
		if strings.Contains(tok, "Bearer abc") {
			t.Errorf("authorization leaked: %v", out)
		}
	}
}

func TestSanitiseURL_stripsUserinfoAndPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"http://localhost:9200", "http://localhost:9200"},
		{"https://user:pw@es.example.com:9200/path?q=1", "https://es.example.com:9200"},
		{"http://es.example.com", "http://es.example.com"},
		{"", ""},
		{"not-a-url", ""},
	}
	for _, c := range cases {
		got := SanitiseURL(c.in)
		if got != c.want {
			t.Errorf("SanitiseURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitiseURL_neverLeavesCredentialsInRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")

	Reset()
	Init(path)
	Begin("ops health",
		[]string{"esops", "ops", "health"},
		"prod-eu",
		"https://elastic:hunter2@es-prod-eu.example.com:9200/cluster?foo=bar",
		false,
	)
	Finish(0, "")

	data, err := os.ReadFile(path) // #nosec G304 -- test-local path under TempDir
	if err != nil {
		t.Fatalf("read audit file: %v", err)
	}
	got := string(data)
	for _, leak := range []string{"hunter2", "elastic:hunter2", "/cluster", "foo=bar"} {
		if strings.Contains(got, leak) {
			t.Errorf("audit file leaked %q in: %s", leak, got)
		}
	}
	if !strings.Contains(got, "https://es-prod-eu.example.com:9200") {
		t.Errorf("sanitised cluster_url missing from audit file: %s", got)
	}
}

func TestRedaction_endToEnd_secretFlagsAndURLCredentialsDoNotReachFile(t *testing.T) {
	// Roadmap §11-style redaction test for the audit log: fire an
	// invocation with a known-bad auth header on the command line and
	// a credential embedded in the URL. Neither must appear in the
	// written audit file.
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")

	Reset()
	Init(path)
	Begin(
		"ops health",
		[]string{"esops", "--password", "hunter2", "--api-key=ak-1234", "--token", "bearer-xyz", "ops", "health"},
		"prod-eu",
		"https://admin:supersecret@es-prod-eu.example.com:9200",
		false,
	)
	Summary(map[string]any{"note": "non-secret summary fields are fine"})
	Finish(0, "")

	data, err := os.ReadFile(path) // #nosec G304 -- test-local path under TempDir
	if err != nil {
		t.Fatalf("read audit file: %v", err)
	}
	got := string(data)
	for _, leak := range []string{"hunter2", "ak-1234", "bearer-xyz", "supersecret", "admin:supersecret"} {
		if strings.Contains(got, leak) {
			t.Errorf("audit file leaked %q in: %s", leak, got)
		}
	}
}

func TestExpandPath_tildeAndEnv(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("UserHomeDir failed: %v", err)
	}
	t.Setenv("ESOPS_AUDIT_TEST_PREFIX", "/tmp/esops-test")

	cases := []struct {
		in, want string
	}{
		{"~/audit.log", filepath.Join(home, "audit.log")},
		{"~", home},
		{"${ESOPS_AUDIT_TEST_PREFIX}/audit.log", "/tmp/esops-test/audit.log"},
		{"$ESOPS_AUDIT_TEST_PREFIX/audit.log", "/tmp/esops-test/audit.log"},
		{"/var/log/esops/audit.log", "/var/log/esops/audit.log"},
	}
	for _, c := range cases {
		got, err := ExpandPath(c.in)
		if err != nil {
			t.Errorf("ExpandPath(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ExpandPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRedaction_resolvedEnvSecretNeverReachesAuditFile is the
// §11-mandated round-trip test: configure a context whose auth carries
// a ${env:VAR} reference, resolve it through the real config loader
// (matching what cmdsetup does), and assert the resolved secret value
// never appears in the audit record. The audit code path never
// inspects the resolved Auth struct — Begin only sees the cluster URL
// and a redacted argv — so this test guards against future regression
// where someone might thread an Auth into the record.
func TestRedaction_resolvedEnvSecretNeverReachesAuditFile(t *testing.T) {
	dir := t.TempDir()
	auditPath := filepath.Join(dir, "audit.log")
	const resolvedSecret = "s3cret-resolved-through-env"
	t.Setenv("ESOPS_AUDIT_TEST_PASSWORD", resolvedSecret)

	cfg := config.Config{
		CurrentContext: "test-ctx",
		Contexts: map[string]config.Context{
			"test-ctx": {
				URL: "http://localhost:9200",
				Auth: config.Auth{
					Type:     "basic",
					Username: "tester",
					Password: "${env:ESOPS_AUDIT_TEST_PASSWORD}",
				},
			},
		},
	}
	_, cc, err := cfg.ResolveContext("test-ctx")
	if err != nil {
		t.Fatalf("ResolveContext: %v", err)
	}
	if cc.Auth.Password != resolvedSecret {
		// Sanity check: confirm the indirection actually resolved.
		// If this fails the test below is meaningless because we'd
		// be searching for a string that was never in the system.
		t.Fatalf("env indirection did not resolve; got %q", cc.Auth.Password)
	}

	Reset()
	Init(auditPath)
	Begin(
		"ops health",
		[]string{"esops", "--context", "test-ctx", "ops", "health"},
		"test-ctx",
		cc.URL,
		false,
	)
	Finish(0, "")

	data, err := os.ReadFile(auditPath) // #nosec G304 -- test-local path under TempDir
	if err != nil {
		t.Fatalf("read audit file: %v", err)
	}
	if strings.Contains(string(data), resolvedSecret) {
		t.Errorf("audit file leaked resolved env secret %q: %s", resolvedSecret, data)
	}
}

func TestBegin_recordsDryRunFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")

	Reset()
	Init(path)
	Begin("index optimize", []string{"esops", "index", "optimize", "--dry-run", "logs-*"}, "local-es", "http://localhost:9200", true)
	Finish(0, "")

	records := readRecords(t, path)
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	if !records[0].DryRun {
		t.Errorf("dry_run = false, want true")
	}
}
