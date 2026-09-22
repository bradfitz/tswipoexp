//go:build !windows

package main

// registerSystemProxy is a no-op off Windows, where tswipoexp is only
// built for development.
func registerSystemProxy(addr string, logf func(string, ...any)) error {
	logf("winproxy: not on Windows; not registering %s", addr)
	return nil
}

func unregisterSystemProxy(logf func(string, ...any)) error { return nil }

func systemProxyIsOurs(addr string) bool { return false }
