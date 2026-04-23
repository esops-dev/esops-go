// Package config loads and resolves esops configuration from a YAML file.
//
// The file shape matches config/esops.example.yaml: a set of named
// `contexts`, a `current-context` pointer, and `defaults` that contexts
// inherit. Secret indirection (${env:VAR}, ${file:...}, ${keyring:...})
// is applied at load time. For v0.1.0 only ${env:...} is implemented;
// the other prefixes return an explicit error so users aren't silently
// passed a literal `${file:/...}` string to an auth header.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Config is the parsed esops configuration file.
type Config struct {
	CurrentContext string              `koanf:"current-context"`
	Defaults       Defaults            `koanf:"defaults"`
	Contexts       map[string]Context  `koanf:"contexts"`
}

// Defaults apply to every context unless overridden.
type Defaults struct {
	Output     string        `koanf:"output"`
	LogLevel   string        `koanf:"log_level"`
	LogFormat  string        `koanf:"log_format"`
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
	Timeout     time.Duration `koanf:"timeout"`
	Compress    bool          `koanf:"compress"`
}

// Auth carries credentials for a context. Only `basic` is wired up in
// v0.1.0 — the other fields are accepted by the parser so configs are
// forward-compatible, but the client factory will reject them.
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

// Load reads and parses a config file and resolves secret indirections.
func Load(path string) (Config, error) {
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
	if err := resolveContextSecrets(&ctx); err != nil {
		return name, ctx, fmt.Errorf("context %q: %w", name, err)
	}
	return name, ctx, nil
}

// secretRef matches ${env:VAR}, ${file:/path}, ${keyring:svc/acct}.
var secretRef = regexp.MustCompile(`^\$\{([a-z]+):([^}]+)\}$`)

func resolveContextSecrets(c *Context) error {
	for _, field := range []*string{&c.Auth.Password, &c.Auth.APIKey, &c.Auth.Token} {
		v, err := resolveOne(*field)
		if err != nil {
			return err
		}
		*field = v
	}
	return nil
}

func resolveOne(v string) (string, error) {
	m := secretRef.FindStringSubmatch(v)
	if m == nil {
		return v, nil
	}
	scheme, arg := m[1], m[2]
	switch scheme {
	case "env":
		val, ok := os.LookupEnv(arg)
		if !ok {
			return "", fmt.Errorf("environment variable %q is not set", arg)
		}
		return val, nil
	case "file", "keyring":
		return "", fmt.Errorf("${%s:...} indirection is not implemented yet", scheme)
	default:
		return "", fmt.Errorf("unknown secret indirection scheme %q", scheme)
	}
}
