package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// This file sets the current user's proxy through WinINet's
// per-connection option API rather than by writing ProxyEnable and
// ProxyServer to the registry. Both WinHTTP users (such as .NET) and
// Chromium-based browsers read the per-connection settings, which
// live in a binary blob under Internet Settings\Connections; writing
// only the legacy registry values leaves that blob saying "direct"
// and gets ignored.

const (
	userEnvKey = `Environment`

	// wininet.h
	internetOptionRefresh             = 37
	internetOptionSettingsChanged     = 39
	internetOptionPerConnectionOption = 75

	internetPerConnFlags         = 1
	internetPerConnProxyServer   = 2
	internetPerConnProxyBypass   = 3
	internetPerConnAutoconfigURL = 4

	// winuser.h
	wmSettingChange  = 0x001A
	hwndBroadcast    = 0xFFFF
	smtoAbortIfHung  = 0x0002
	broadcastTimeout = 5000 // milliseconds
)

var (
	wininet              = windows.NewLazySystemDLL("wininet.dll")
	procInternetSetOpt   = wininet.NewProc("InternetSetOptionW")
	procInternetQueryOpt = wininet.NewProc("InternetQueryOptionW")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalFree       = kernel32.NewProc("GlobalFree")
	user32               = windows.NewLazySystemDLL("user32.dll")
	procSendMessageTOut  = user32.NewProc("SendMessageTimeoutW")
)

// perConnOption mirrors INTERNET_PER_CONN_OPTIONW. The value is a
// union of DWORD, LPWSTR, and FILETIME, so it's 8 bytes and 8-byte
// aligned on amd64 and arm64.
type perConnOption struct {
	option uint32
	_      uint32
	value  uint64 // DWORD in the low bits, or a pointer to UTF-16
}

// perConnOptionList mirrors INTERNET_PER_CONN_OPTION_LISTW.
type perConnOptionList struct {
	size        uint32
	_           uint32
	connection  *uint16 // nil means the default (LAN) connection
	optionCount uint32
	optionError uint32
	options     *perConnOption
}

// proxyBypassList is the WinINet bypass list used while registered.
const proxyBypassList = "localhost;127.*;[::1]"

// registerSystemProxy points the current user's WinINet proxy
// settings and proxy environment variables at addr (a host:port),
// saving the previous values first. Calling it while already
// registered is fine: the original saved values are kept.
func registerSystemProxy(addr string, logf func(string, ...any)) error {
	prev, err := loadSavedProxySettings()
	if err != nil {
		logf("winproxy: reading restore file: %v", err)
	}
	if prev != nil && !prev.isThisMachine() {
		// Settings from another machine can't be restored here.
		// Leave the file alone so that machine can restore them
		// if this stick returns to it, but don't let it confuse
		// this machine's bookkeeping.
		logf("winproxy: restore file is for %s\\%s, not this machine; ignoring", prev.Machine, prev.User)
		prev = nil
	}
	if prev == nil {
		prev, err = captureProxySettings()
		if err != nil {
			return fmt.Errorf("reading current proxy settings: %w", err)
		}
		if err := prev.save(); err != nil {
			return fmt.Errorf("saving proxy restore file: %w", err)
		}
		if err := installCrashRestore(prev); err != nil {
			// Not fatal: the in-process restore still works for a
			// clean exit. But say so, since it's the safety net.
			logf("winproxy: installing crash restore: %v", err)
		}
	}

	// Both http and https go to our HTTP proxy; https uses CONNECT.
	// The socks= rule is deliberately omitted because Chromium reads
	// it as SOCKS4, which our server doesn't speak.
	//
	// The bypass list names loopback explicitly rather than using
	// "<local>", because "<local>" means every hostname without a
	// dot, which would send short MagicDNS names like "myserver"
	// around the proxy and break them.
	err = setPerConnProxy(proxyTypeDirect|proxyTypeProxy, fmt.Sprintf("http=%s;https=%s", addr, addr), proxyBypassList, "")
	if err != nil {
		return fmt.Errorf("InternetSetOption: %w", err)
	}

	url := "http://" + addr
	if err := setUserEnv(map[string]string{
		"HTTP_PROXY":  url,
		"HTTPS_PROXY": url,
		"NO_PROXY":    "localhost,127.0.0.1,::1",
	}); err != nil {
		logf("winproxy: setting environment: %v", err)
	}
	logf("winproxy: registered %s as the user's HTTP/HTTPS proxy", addr)
	return nil
}

// unregisterSystemProxy restores the settings saved by
// registerSystemProxy, if any were saved on this machine.
func unregisterSystemProxy(logf func(string, ...any)) error {
	prev, err := loadSavedProxySettings()
	if err != nil {
		return err
	}
	if prev == nil || !prev.isThisMachine() {
		return nil
	}
	if err := prev.restore(); err != nil {
		return err
	}
	if err := removeSavedProxySettings(); err != nil {
		logf("winproxy: removing restore file: %v", err)
	}
	if err := removeCrashRestore(); err != nil {
		logf("winproxy: removing crash restore: %v", err)
	}
	logf("winproxy: restored previous proxy settings")
	return nil
}

const (
	runOnceKey       = `Software\Microsoft\Windows\CurrentVersion\RunOnce`
	runOnceValueName = "tswipoexp-restore-proxy"
	crashRestoreCmd  = "restore-proxy.cmd"
	crashRestoreReg  = "restore-proxy.reg"
	connectionsKey   = `Software\Microsoft\Windows\CurrentVersion\Internet Settings\Connections`
)

// installCrashRestore writes the restore .reg and .cmd next to the
// restore JSON and registers the .cmd to run once at the user's next
// logon, so an unclean exit still gets the proxy settings put back.
func installCrashRestore(prev *savedProxySettings) error {
	restorePath, err := proxyRestorePath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(restorePath)

	var blob []byte
	if k, err := registry.OpenKey(registry.CURRENT_USER, connectionsKey, registry.QUERY_VALUE); err == nil {
		blob, _, _ = k.GetBinaryValue("DefaultConnectionSettings")
		k.Close()
	}
	regPath := filepath.Join(dir, crashRestoreReg)
	if err := writeFileAtomic(regPath, []byte(restoreRegFile(prev, blob))); err != nil {
		return err
	}
	cmdPath := filepath.Join(dir, crashRestoreCmd)
	if err := writeFileAtomic(cmdPath, []byte(restoreCmdFile(crashRestoreReg))); err != nil {
		return err
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runOnceKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(runOnceValueName, fmt.Sprintf(`cmd.exe /c "%s"`, cmdPath))
}

// removeCrashRestore deletes the RunOnce entry and its files after a
// clean restore.
func removeCrashRestore() error {
	if k, err := registry.OpenKey(registry.CURRENT_USER, runOnceKey, registry.SET_VALUE); err == nil {
		if err := k.DeleteValue(runOnceValueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
			k.Close()
			return err
		}
		k.Close()
	}
	restorePath, err := proxyRestorePath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(restorePath)
	for _, name := range []string{crashRestoreCmd, crashRestoreReg} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *savedProxySettings) isThisMachine() bool {
	m, u := machineAndUser()
	return s.Machine == m && s.User == u
}

func machineAndUser() (machine, user string) {
	machine, _ = os.Hostname()
	return machine, os.Getenv("USERNAME")
}

// captureProxySettings reads the current user's settings.
func captureProxySettings() (*savedProxySettings, error) {
	s := new(savedProxySettings)
	s.Machine, s.User = machineAndUser()
	flags, server, bypass, pac, err := queryPerConnProxy()
	if err != nil {
		return nil, err
	}
	s.ProxyFlags = flags
	s.ProxyServer = server
	s.ProxyOverride = bypass
	s.AutoConfigURL = pac
	s.Env = map[string]string{}
	ek, err := registry.OpenKey(registry.CURRENT_USER, userEnvKey, registry.QUERY_VALUE)
	if err == nil {
		defer ek.Close()
		for _, name := range proxyEnvVars {
			if v, _, err := ek.GetStringValue(name); err == nil {
				s.Env[name] = v
			}
		}
	}
	return s, nil
}

// restore writes the saved settings back.
func (s *savedProxySettings) restore() error {
	flags := s.ProxyFlags
	if flags == 0 {
		flags = proxyTypeDirect
	}
	if err := setPerConnProxy(flags, s.ProxyServer, s.ProxyOverride, s.AutoConfigURL); err != nil {
		return err
	}
	env := map[string]string{}
	for _, name := range proxyEnvVars {
		env[name] = "" // delete unless saved below
	}
	for name, v := range s.Env {
		env[name] = v
	}
	return setUserEnv(env)
}

// setPerConnProxy applies proxy settings to the default connection
// and tells WinINet, and everything watching it, that they changed.
// Empty strings clear the corresponding setting.
func setPerConnProxy(flags uint32, server, bypass, pac string) error {
	opts := []perConnOption{{option: internetPerConnFlags, value: uint64(flags)}}
	var keep []*uint16 // keeps the UTF-16 buffers alive across the call
	addStr := func(opt uint32, s string) error {
		p, err := syscall.UTF16PtrFromString(s)
		if err != nil {
			return err
		}
		keep = append(keep, p)
		opts = append(opts, perConnOption{option: opt, value: uint64(uintptr(unsafe.Pointer(p)))})
		return nil
	}
	if err := addStr(internetPerConnProxyServer, server); err != nil {
		return err
	}
	if err := addStr(internetPerConnProxyBypass, bypass); err != nil {
		return err
	}
	if err := addStr(internetPerConnAutoconfigURL, pac); err != nil {
		return err
	}
	list := perConnOptionList{
		optionCount: uint32(len(opts)),
		options:     &opts[0],
	}
	list.size = uint32(unsafe.Sizeof(list))
	r, _, e := procInternetSetOpt.Call(0, internetOptionPerConnectionOption, uintptr(unsafe.Pointer(&list)), uintptr(list.size))
	if r == 0 {
		return fmt.Errorf("per-connection option: %v", e)
	}
	procInternetSetOpt.Call(0, internetOptionSettingsChanged, 0, 0)
	procInternetSetOpt.Call(0, internetOptionRefresh, 0, 0)
	return nil
}

// queryPerConnProxy reads the default connection's proxy settings.
func queryPerConnProxy() (flags uint32, server, bypass, pac string, err error) {
	opts := []perConnOption{
		{option: internetPerConnFlags},
		{option: internetPerConnProxyServer},
		{option: internetPerConnProxyBypass},
		{option: internetPerConnAutoconfigURL},
	}
	list := perConnOptionList{
		optionCount: uint32(len(opts)),
		options:     &opts[0],
	}
	list.size = uint32(unsafe.Sizeof(list))
	size := list.size
	r, _, e := procInternetQueryOpt.Call(0, internetOptionPerConnectionOption, uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		return 0, "", "", "", fmt.Errorf("InternetQueryOption per-connection: %v", e)
	}
	flags = uint32(opts[0].value)
	str := func(o *perConnOption) string {
		if o.value == 0 {
			return ""
		}
		// The value is a pointer to memory WinINet allocated with
		// GlobalAlloc, which we must free. Reading the union field
		// as a pointer type keeps vet's unsafeptr check happy.
		p := *(*unsafe.Pointer)(unsafe.Pointer(&o.value))
		s := windows.UTF16PtrToString((*uint16)(p))
		procGlobalFree.Call(uintptr(p))
		return s
	}
	return flags, str(&opts[1]), str(&opts[2]), str(&opts[3]), nil
}

// setUserEnv sets per-user environment variables in the registry (an
// empty value deletes the variable) and broadcasts the change so
// newly launched programs see it. Already running programs don't.
func setUserEnv(vars map[string]string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, userEnvKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	for name, v := range vars {
		if v == "" {
			if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
				return err
			}
			continue
		}
		if err := k.SetStringValue(name, v); err != nil {
			return err
		}
	}
	env, err := syscall.UTF16PtrFromString("Environment")
	if err != nil {
		return err
	}
	procSendMessageTOut.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(env)), smtoAbortIfHung, broadcastTimeout, 0)
	return nil
}

// systemProxyIsOurs reports whether the user's proxy is currently
// pointed at addr.
func systemProxyIsOurs(addr string) bool {
	flags, server, _, _, err := queryPerConnProxy()
	if err != nil {
		return false
	}
	return flags&proxyTypeProxy != 0 && strings.Contains(server, addr)
}
