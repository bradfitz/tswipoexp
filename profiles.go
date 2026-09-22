package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// stateDirName is the name of the directory next to the executable
// that holds all profiles.
const stateDirName = "tswipoexp-state"

// activeProfileFile is the file inside the state directory naming the
// profile to use at startup.
const activeProfileFile = "active-profile.txt"

// defaultProfileName is the profile created when the state directory
// has none.
const defaultProfileName = "Default"

// configFileName is the per-profile settings file.
const configFileName = "tswipoexp.json"

// defaultHostname is the tailnet hostname used when a profile has not
// configured one.
const defaultHostname = "tswipoexp"

// defaultProxyAddr is the loopback address the SOCKS5 and HTTP proxy
// listens on unless a profile configures something else.
const defaultProxyAddr = "127.0.0.1:1055"

// Config is the per-profile settings file. Tailscale preferences
// (hostname as seen by control, shields up, exit node) live in
// tsnet's own state; this holds what we need before tsnet starts and
// what tsnet has no concept of.
type Config struct {
	// Hostname is the tailnet hostname to use. Empty means
	// defaultHostname.
	Hostname string `json:",omitempty"`

	// SyncHostname, if true, makes Hostname follow the computer's
	// name at each startup.
	SyncHostname bool `json:",omitempty"`

	// ProxyAddr is the loopback address for the SOCKS5 and HTTP
	// proxy. Empty means defaultProxyAddr.
	ProxyAddr string `json:",omitempty"`

	// RegisterProxy, if non-nil and false, disables registering the
	// proxy with the Windows user session. It defaults to on.
	RegisterProxy *bool `json:",omitempty"`
}

func (c *Config) hostname() string {
	if c.SyncHostname {
		if h, err := os.Hostname(); err == nil && h != "" {
			return strings.ToLower(h)
		}
	}
	if c.Hostname != "" {
		return c.Hostname
	}
	return defaultHostname
}

func (c *Config) proxyAddr() string {
	if c.ProxyAddr != "" {
		return c.ProxyAddr
	}
	return defaultProxyAddr
}

func (c *Config) registerProxy() bool {
	return c.RegisterProxy == nil || *c.RegisterProxy
}

// Profiles manages the state directory and the profiles within it.
type Profiles struct {
	// Root is the state directory.
	Root string
}

// exeStateDir returns the state directory next to the running
// executable.
func exeStateDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), stateDirName), nil
}

// List returns the profile names, sorted, creating the default one
// if none exist.
func (p *Profiles) List() ([]string, error) {
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(p.Root)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() && validProfileName(e.Name()) {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		if err := p.Create(defaultProfileName); err != nil {
			return nil, err
		}
		names = []string{defaultProfileName}
	}
	sort.Strings(names)
	return names, nil
}

// validProfileName reports whether name is usable as a profile name,
// which is to say usable as a single directory name on Windows.
func validProfileName(name string) bool {
	if name == "" || len(name) > 64 || name != strings.TrimSpace(name) {
		return false
	}
	if strings.ContainsAny(name, `\/:*?"<>|`) || strings.HasPrefix(name, ".") {
		return false
	}
	return true
}

// Dir returns the directory of the named profile.
func (p *Profiles) Dir(name string) string {
	return filepath.Join(p.Root, name)
}

// Create makes a new empty profile.
func (p *Profiles) Create(name string) error {
	if !validProfileName(name) {
		return fmt.Errorf("invalid profile name %q", name)
	}
	dir := p.Dir(name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("profile %q already exists", name)
	}
	return os.MkdirAll(dir, 0o700)
}

// Active returns the profile named in active-profile.txt, falling
// back to the first profile in List order when the file is missing or
// names a profile that no longer exists.
func (p *Profiles) Active() (string, error) {
	names, err := p.List()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(p.Root, activeProfileFile))
	if err == nil {
		want := strings.TrimSpace(string(b))
		for _, n := range names {
			if n == want {
				return n, nil
			}
		}
	}
	return names[0], nil
}

// SetActive records name as the profile to use at the next startup.
func (p *Profiles) SetActive(name string) error {
	if !validProfileName(name) {
		return fmt.Errorf("invalid profile name %q", name)
	}
	return writeFileAtomic(filepath.Join(p.Root, activeProfileFile), []byte(name+"\n"))
}

// LoadConfig reads a profile's settings. A missing file yields the
// zero Config.
func (p *Profiles) LoadConfig(name string) (*Config, error) {
	c := new(Config)
	b, err := os.ReadFile(filepath.Join(p.Dir(name), configFileName))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", configFileName, err)
	}
	return c, nil
}

// SaveConfig writes a profile's settings.
func (p *Profiles) SaveConfig(name string, c *Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(p.Dir(name), configFileName), append(b, '\n'))
}

// writeFileAtomic writes data to path via a temp file and rename so a
// yanked USB stick leaves either the old or the new contents.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
