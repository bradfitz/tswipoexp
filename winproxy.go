package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// PROXY_TYPE_* flags from wininet.h, as stored in the per-connection
// settings and in savedProxySettings.ProxyFlags.
const (
	proxyTypeDirect       = 0x1
	proxyTypeProxy        = 0x2
	proxyTypeAutoProxyURL = 0x4
	proxyTypeAutoDetect   = 0x8
)

// proxyRegistration describes how to register the proxy with the
// user's Windows session. It's derived from the profile Config.
type proxyRegistration struct {
	Addr     string // proxy host:port
	Mode     string // proxyModePAC or proxyModeStatic
	PACURL   string // URL of the PAC script, for PAC mode
	EnvVars  bool   // also set HTTP_PROXY and friends
	Watchdog bool   // start a watchdog that restores on death
	RunOnce  bool   // register a RunOnce entry that restores at logon
}

// runOnceValueName is the per-user RunOnce entry that runs the restore
// script at the next logon.
const runOnceValueName = "tswipoexp-restore-proxy"

// pacPath is where the proxy's HTTP side serves the PAC script.
const pacPath = "/proxy.pac"

// proxyEnvVars are the per-user environment variables set to point
// at the proxy. Many command line tools honor these where they don't
// honor WinINet settings.
var proxyEnvVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"}

// proxyRestoreFile is the machine-local file remembering the proxy
// settings that were in place before tswipoexp registered its own,
// so they can be put back at exit or, after a crash or yanked USB
// stick, at the next start on the same machine.
const proxyRestoreFile = "proxy-restore.json"

// savedProxySettings is what registerSystemProxy saves so that
// unregisterSystemProxy can restore it.
type savedProxySettings struct {
	// Machine and User identify where these settings came from,
	// since the state directory may travel between computers.
	Machine string
	User    string

	// WinINet per-connection settings for the default connection.
	// ProxyFlags is the PROXY_TYPE_* bit set.
	ProxyFlags    uint32
	ProxyServer   string
	ProxyOverride string
	AutoConfigURL string

	// Env holds the previous per-user environment variables that
	// were replaced; a missing key means it wasn't set.
	Env map[string]string
}

// proxyRestorePath returns where the restore file lives on this
// machine. Because it's about this machine's settings, it lives in
// the machine's local app data, not on the (possibly roaming) state
// directory. This is the one deliberate exception to keeping all
// state next to the binary.
func proxyRestorePath() (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		var err error
		base, err = os.UserCacheDir()
		if err != nil {
			return "", err
		}
	}
	dir := filepath.Join(base, "tswipoexp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, proxyRestoreFile), nil
}

func loadSavedProxySettings() (*savedProxySettings, error) {
	p, err := proxyRestorePath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s := new(savedProxySettings)
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *savedProxySettings) save() error {
	p, err := proxyRestorePath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(p, b)
}

func removeSavedProxySettings() error {
	p, err := proxyRestorePath()
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
