// Package config loads and resolves esops configuration from a YAML file.
//
// The file shape matches config/esops.example.yaml: a set of named
// `contexts`, a `current-context` pointer, and `defaults` that contexts
// inherit. Secret indirection (${env:VAR}, ${file:...}, ${keyring:...})
// is resolved when ResolveContext is called.
package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/zalando/go-keyring"
)

// loggerMu guards the package-level logger that emits safety warnings
// during Load. It defaults to a discard handler so library consumers
// (esops-doctor, future commercial product) don't see stray output; the
// esops CLI installs the process-wide logger via SetLogger during init.
var (
	loggerMu sync.RWMutex
	logger   = slog.New(slog.NewTextHandler(io.Discard, nil))
)

// SetLogger installs the slog.Logger used for safety warnings emitted
// from Load (world-readable config + plaintext secret, plaintext
// secrets in any mode). Passing nil resets to a discard handler.
func SetLogger(l *slog.Logger) {
	loggerMu.Lock()
	defer loggerMu.Unlock()
	if l == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
		return
	}
	logger = l
}

func getLogger() *slog.Logger {
	loggerMu.RLock()
	defer loggerMu.RUnlock()
	return logger
}

// Config is the parsed esops configuration file.
type Config struct {
	CurrentContext string             `koanf:"current-context"`
	Defaults       Defaults           `koanf:"defaults"`
	Contexts       map[string]Context `koanf:"contexts"`
}

// Defaults apply to every context unless overridden.
type Defaults struct {
	Output     string        `koanf:"output"`
	LogLevel   string        `koanf:"log_level"`
	LogFormat  string        `koanf:"log_format"`
	LogFile    string        `koanf:"log_file"`
	AuditFile  string        `koanf:"audit_file"`
	Timeout    time.Duration `koanf:"timeout"`
	MaxRetries int           `koanf:"max_retries"`
	Compress   bool          `koanf:"compress"`
}

// Context describes how to reach a single cluster.
type Context struct {
	URL         string        `koanf:"url"`
	URLs        []string      `koanf:"urls"`
	Auth        Auth          `koanf:"auth"`
	TLS         TLS           `koanf:"tls"`
	Protection  string        `koanf:"protection"`
	MaxInFlight int           `koanf:"max_in_flight"`
	MaxRetries  int           `koanf:"max_retries"`
	Timeout     time.Duration `koanf:"timeout"`
	Compress    bool          `koanf:"compress"`
}

// Auth carries credentials for a context. The `type` field selects the
// method; required and forbidden fields per type are enforced by
// ValidateAuth at config load. Indirection (${env:VAR} etc.) on
// password / api_key / token is resolved later, in ResolveContext.
type Auth struct {
	Type       string `koanf:"type"`
	Username   string `koanf:"username"`
	Password   string `koanf:"password"`
	APIKey     string `koanf:"api_key"`
	Token      string `koanf:"token"`
	ClientCert string `koanf:"client_cert"`
	ClientKey  string `koanf:"client_key"`
	Region     string `koanf:"region"`
	Service    string `koanf:"service"`
}

// TLS overrides for a context. `insecure` is the last-resort escape hatch.
type TLS struct {
	CACert   string `koanf:"cacert"`
	Insecure bool   `koanf:"insecure"`
}

// Addresses returns the effective list of URLs for the context,
// preferring `urls` over the single `url` when both are set.
func (c Context) Addresses() []string {
	if len(c.URLs) > 0 {
		return c.URLs
	}
	if c.URL != "" {
		return []string{c.URL}
	}
	return nil
}

// LoadDefault resolves the effective config path (honouring `explicit`
// if non-empty, otherwise searching the standard lookup order from
// Resolve) and loads it. Returns the resolved path alongside the
// parsed Config so callers can reference it in messages or write-back.
func LoadDefault(explicit string) (Config, string, error) {
	path, err := Resolve(explicit)
	if err != nil {
		return Config{}, "", err
	}
	cfg, err := Load(path)
	return cfg, path, err
}

// Resolve returns the effective config path. If `explicit` is
// non-empty, it is returned unchanged — the caller asked for this
// specific file. Otherwise the standard lookup order is searched and
// the first existing path wins:
//
//  1. $ESOPS_CONFIG                               (env)
//  2. ./esops.yaml                                (per-repo)
//  3. $XDG_CONFIG_HOME/esops/config.yaml          (XDG)
//  4. ~/.config/esops/config.yaml                 (XDG fallback)
//
// If none of those exist, an error listing the searched paths is
// returned. $ESOPS_CONFIG must point at an existing file; a missing
// env-specified file is an error rather than a silent fallthrough.
func Resolve(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if env := os.Getenv("ESOPS_CONFIG"); env != "" {
		if _, err := os.Stat(env); err != nil { // #nosec G304,G703 -- ESOPS_CONFIG points at the user's chosen config file
			return "", fmt.Errorf("ESOPS_CONFIG=%q: %w", env, err)
		}
		return env, nil
	}
	candidates := searchPaths()
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no esops config found; searched:\n  %s\npass --config PATH, set ESOPS_CONFIG, or create ~/.config/esops/config.yaml",
		strings.Join(candidates, "\n  "))
}

// searchPaths returns the lookup candidates in resolution order,
// excluding the $ESOPS_CONFIG env var (handled separately because a
// set-but-missing env path is a hard error, not a fallthrough).
func searchPaths() []string {
	var paths []string
	paths = append(paths, "esops.yaml")
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		paths = append(paths, filepath.Join(x, "esops", "config.yaml"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "esops", "config.yaml"))
	}
	return paths
}

// Parse reads and unmarshals a config file without running the safety
// checks performed by Load. Intended for early-stage callers (the CLI
// Before hook reading Defaults for logger init) where warnings can't
// yet reach an initialised logger — Load will re-check and surface them
// when the command action runs.
func Parse(path string) (Config, error) {
	k := koanf.New(".")
	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		return Config{}, fmt.Errorf("loading config %q: %w", path, err)
	}
	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing config: %w", err)
	}
	return cfg, nil
}

// Load reads and parses a config file and runs safety checks:
// world-readable files with inline plaintext secrets are refused unless
// ESOPS_ALLOW_INSECURE_CONFIG=1; plaintext secrets in any mode produce a
// warning unless ESOPS_ALLOW_PLAINTEXT_SECRETS=1.
//
// Per-context auth blocks are also validated (required/forbidden
// fields per type) before secrets are flagged — the check is purely
// structural and runs against parsed values, so a `password:
// ${env:VAR}` reads as "password set" regardless of whether VAR is
// exported yet. Dialect-specific rejections (api_key on OS, sigv4 on
// ES) happen at adapter construction once the probe knows the cluster.
func Load(path string) (Config, error) {
	cfg, err := Parse(path)
	if err != nil {
		return Config{}, err
	}
	if err := validateAllAuth(cfg); err != nil {
		return Config{}, err
	}
	if err := checkSafety(path, cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// checkSafety enforces the two config-secrecy rules:
//   - world-readable file + inline plaintext secret = refuse (unless
//     ESOPS_ALLOW_INSECURE_CONFIG=1)
//   - inline plaintext secret in any mode = warn (unless
//     ESOPS_ALLOW_PLAINTEXT_SECRETS=1)
//
// It inspects the *parsed* (pre-resolution) config so ${env:...} etc.
// are recognised as indirection and not flagged as plaintext.
func checkSafety(path string, cfg Config) error {
	plaintextCtxs := ContextsWithPlaintextSecret(cfg)
	worldReadable, err := IsWorldReadable(path)
	if err != nil {
		return fmt.Errorf("checking config permissions: %w", err)
	}

	if worldReadable && len(plaintextCtxs) > 0 {
		if os.Getenv("ESOPS_ALLOW_INSECURE_CONFIG") != "1" {
			return fmt.Errorf(
				"config %q is world-readable and contains inline plaintext secrets for context(s) [%s]; "+
					"tighten permissions (chmod 600 %s) or set ESOPS_ALLOW_INSECURE_CONFIG=1 to override",
				path, strings.Join(plaintextCtxs, ", "), path)
		}
		getLogger().Warn("config.insecure",
			"path", path,
			"contexts", plaintextCtxs,
			"hint", "world-readable with plaintext secrets; override via ESOPS_ALLOW_INSECURE_CONFIG=1",
		)
		return nil
	}

	if len(plaintextCtxs) > 0 && os.Getenv("ESOPS_ALLOW_PLAINTEXT_SECRETS") != "1" {
		getLogger().Warn("config.plaintext_secrets",
			"path", path,
			"contexts", plaintextCtxs,
			"hint", "use ${env:VAR} indirection, or set ESOPS_ALLOW_PLAINTEXT_SECRETS=1 to silence",
		)
	}
	return nil
}

// IsWorldReadable reports whether the file at path has its world-read
// mode bit set. On Windows, where os.Stat's mode does not reflect real
// ACLs, always returns false — the check would be misleading.
//
// Exported so callers that lint the same on-disk shape (the CLI's
// `config validate`) can apply identical rules to the runtime loader.
// Drift between the loader's safety check and any external lint is a
// bug; sharing this function is how we prevent it.
func IsWorldReadable(path string) (bool, error) {
	if runtime.GOOS == "windows" {
		return false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return info.Mode().Perm()&0o004 != 0, nil
}

// ContextsWithPlaintextSecret returns the names of contexts whose auth
// block contains at least one non-empty secret field that isn't an
// indirection reference. Sorted for stable warnings.
//
// Exported alongside HasPlaintextSecret so external linters (e.g.
// `config validate`) share the loader's plaintext detection rather
// than replicating it. See IsWorldReadable for the drift rationale.
func ContextsWithPlaintextSecret(cfg Config) []string {
	var names []string
	for name, c := range cfg.Contexts {
		if HasPlaintextSecret(c.Auth) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// HasPlaintextSecret reports whether the auth block carries an inline
// secret (a non-empty password / api_key / token that isn't an
// indirection reference). ClientCert/ClientKey are filesystem paths,
// not secret material — the key's contents stay on disk, so a literal
// path here is not a leaked credential.
//
// Exported for the same reason as ContextsWithPlaintextSecret.
func HasPlaintextSecret(a Auth) bool {
	for _, v := range []string{a.Password, a.APIKey, a.Token} {
		if v == "" {
			continue
		}
		if IsSecretRef(v) {
			continue
		}
		return true
	}
	return false
}

// IsSecretRef reports whether s matches the indirection reference
// syntax — ${env:VAR}, ${file:/path}, ${keyring:svc/acct}. Exported so
// any caller that distinguishes literal secrets from pointers to
// secrets (the loader's plaintext detection, the CLI's `config view`
// redaction, the CLI's `config validate` lint) shares one definition.
func IsSecretRef(s string) bool {
	return secretRef.MatchString(s)
}

// ParseSecretRef splits an indirection reference into its scheme and
// argument — ${env:VAR} → ("env", "VAR"), ${keyring:svc/acct} →
// ("keyring", "svc/acct"). Returns ok=false for anything that isn't a
// reference. Exported alongside IsSecretRef so external linters share
// the loader's parser instead of replicating the regex.
func ParseSecretRef(s string) (scheme, arg string, ok bool) {
	m := secretRef.FindStringSubmatch(s)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// ResolveContext returns the effective Context for `name`, or for
// cfg.CurrentContext if `name` is empty. Defaults are merged in where
// the context hasn't overridden them.
func (cfg Config) ResolveContext(name string) (string, Context, error) {
	if name == "" {
		name = cfg.CurrentContext
	}
	if name == "" {
		return "", Context{}, errors.New("no context specified and no current-context set in config")
	}
	ctx, ok := cfg.Contexts[name]
	if !ok {
		return "", Context{}, fmt.Errorf("context %q not found in config", name)
	}
	if ctx.Timeout == 0 {
		ctx.Timeout = cfg.Defaults.Timeout
	}
	if !ctx.Compress {
		ctx.Compress = cfg.Defaults.Compress
	}
	if ctx.MaxRetries == 0 {
		ctx.MaxRetries = cfg.Defaults.MaxRetries
	}
	if err := resolveContextSecrets(&ctx); err != nil {
		return name, ctx, fmt.Errorf("context %q: %w", name, err)
	}
	return name, ctx, nil
}

// secretRef matches ${env:VAR}, ${file:/path}, ${keyring:svc/acct}.
var secretRef = regexp.MustCompile(`^\$\{([a-z]+):([^}]+)\}$`)

// ResolveAuth returns a copy of a with password, api_key, and token
// indirection references resolved (${env:VAR}, ${file:/path},
// ${keyring:svc/acct}). Literal values pass through unchanged. The
// first failed resolution short-circuits and is returned to the caller
// — partial resolution is never returned.
//
// Exposed for callers that want to render or transmit resolved
// credentials without reaching for ResolveContext (which also merges
// defaults). The CLI's `config view --show-secrets` is the canonical
// consumer.
func ResolveAuth(a Auth) (Auth, error) {
	for _, field := range []*string{&a.Password, &a.APIKey, &a.Token} {
		v, err := resolveOne(*field)
		if err != nil {
			return Auth{}, err
		}
		*field = v
	}
	return a, nil
}

func resolveContextSecrets(c *Context) error {
	resolved, err := ResolveAuth(c.Auth)
	if err != nil {
		return err
	}
	c.Auth = resolved
	return nil
}

func resolveOne(v string) (string, error) {
	scheme, arg, ok := ParseSecretRef(v)
	if !ok {
		return v, nil
	}
	switch scheme {
	case "env":
		val, ok := os.LookupEnv(arg)
		if !ok {
			return "", fmt.Errorf("environment variable %q is not set", arg)
		}
		return val, nil
	case "file":
		// #nosec G304 -- arg is the operator's chosen secret-file path from their own config.
		data, err := os.ReadFile(arg)
		if err != nil {
			return "", fmt.Errorf("reading secret file %q: %w", arg, err)
		}
		warnIfWorldReadable(arg)
		return strings.TrimRight(string(data), "\r\n"), nil
	case "keyring":
		service, account, ok := strings.Cut(arg, "/")
		if !ok || service == "" || account == "" {
			return "", fmt.Errorf("keyring reference %q must be in the form service/account", arg)
		}
		val, err := keyring.Get(service, account)
		if err != nil {
			return "", fmt.Errorf("reading keyring entry %q: %w", arg, err)
		}
		return val, nil
	default:
		return "", fmt.Errorf("unknown secret indirection scheme %q", scheme)
	}
}

// warnIfWorldReadable emits a single slog warning when path's mode bits
// have the world-read bit set, unless the operator has opted out via
// ESOPS_ALLOW_INSECURE_CONFIG=1. The check never refuses — Kubernetes
// and Docker secret mounts default to mode 0644, so refusing would
// break the most common production setup. The warning still flags
// genuine permission mistakes (e.g. a developer dropping a token into
// ~/secrets without tightening it). Skipped on Windows for the same
// reason as the config-file check: os.Stat's mode bits don't reflect
// real ACLs there.
func warnIfWorldReadable(path string) {
	if runtime.GOOS == "windows" {
		return
	}
	if os.Getenv("ESOPS_ALLOW_INSECURE_CONFIG") == "1" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if info.Mode().Perm()&0o004 == 0 {
		return
	}
	getLogger().Warn("config.secret_file_world_readable",
		"path", path,
		"mode", fmt.Sprintf("%#o", info.Mode().Perm()),
		"hint", "tighten with chmod 0600 (k8s/docker secret mounts can be ignored; set ESOPS_ALLOW_INSECURE_CONFIG=1 to silence)",
	)
}
