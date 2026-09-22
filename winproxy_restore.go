package main

import (
	"fmt"
	"strings"
)

// This file generates the crash-safe restore script. tswipoexp
// registers itself as the user's proxy while running and undoes that
// at exit, but an unexpected shutdown (Windows Update rebooting the
// machine, a pulled USB stick) leaves the user's proxy pointed at a
// dead port with nothing around to fix it. So before registering,
// the previous settings are written out as .reg files plus a .cmd
// that imports them, and a per-user RunOnce entry points at the
// .cmd. Windows runs and deletes RunOnce entries at the next logon,
// needs no admin for HKCU, and the script needs nothing from the
// stick. A clean exit removes the entry and the files instead.

const (
	regInternetSettings = `HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	regConnections      = regInternetSettings + `\Connections`
	regEnvironment      = `HKEY_CURRENT_USER\Environment`
)

// restoreRegFile renders a .reg file that puts the saved WinINet
// values, the per-connection blob, and the environment variables
// back. A nil blob leaves the connection blob alone.
func restoreRegFile(s *savedProxySettings, connBlob []byte) string {
	var b strings.Builder
	b.WriteString("Windows Registry Editor Version 5.00\r\n\r\n")

	b.WriteString("[" + regInternetSettings + "]\r\n")
	enable := 0
	if s.ProxyFlags&proxyTypeProxy != 0 {
		enable = 1
	}
	fmt.Fprintf(&b, "\"ProxyEnable\"=dword:%08x\r\n", enable)
	writeRegString(&b, "ProxyServer", s.ProxyServer)
	writeRegString(&b, "ProxyOverride", s.ProxyOverride)
	writeRegString(&b, "AutoConfigURL", s.AutoConfigURL)
	b.WriteString("\r\n")

	if connBlob != nil {
		b.WriteString("[" + regConnections + "]\r\n")
		b.WriteString("\"DefaultConnectionSettings\"=hex:")
		for i, c := range connBlob {
			if i > 0 {
				b.WriteString(",")
				if i%24 == 0 {
					b.WriteString("\\\r\n  ")
				}
			}
			fmt.Fprintf(&b, "%02x", c)
		}
		b.WriteString("\r\n\r\n")
	}

	b.WriteString("[" + regEnvironment + "]\r\n")
	for _, name := range proxyEnvVars {
		writeRegString(&b, name, s.Env[name])
	}
	b.WriteString("\r\n")
	return b.String()
}

// writeRegString writes a REG_SZ value, or a deletion when the value
// is empty, since the original may not have existed at all and an
// empty ProxyServer is not the same as a missing one.
func writeRegString(b *strings.Builder, name, val string) {
	if val == "" {
		fmt.Fprintf(b, "\"%s\"=-\r\n", name)
		return
	}
	fmt.Fprintf(b, "\"%s\"=\"%s\"\r\n", name, regEscape(val))
}

func regEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// restoreCmdFile renders the batch file the watchdog and RunOnce
// execute. It imports the .reg file next to it, then removes the
// RunOnce entry, the JSON snapshot (the settings are back, so the
// next start has nothing to restore), and both files. %~dp0 is the
// script's own directory.
func restoreCmdFile(regName string) string {
	return "@echo off\r\n" +
		"rem Written by tswipoexp before it registered itself as this user's proxy.\r\n" +
		"rem If you're seeing this, tswipoexp didn't get to exit cleanly; this puts\r\n" +
		"rem the previous proxy settings back. It runs once and removes itself.\r\n" +
		"reg import \"%~dp0" + regName + "\" >nul 2>&1\r\n" +
		"reg delete \"HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\RunOnce\" /v " + runOnceValueName + " /f >nul 2>&1\r\n" +
		"del \"%~dp0" + proxyRestoreFile + "\" >nul 2>&1\r\n" +
		"del \"%~dp0" + regName + "\" >nul 2>&1\r\n" +
		// The (goto) trick ends the batch file without cmd trying
		// to read the next line from the file it just deleted,
		// which otherwise prints "The batch file cannot be found."
		"(goto) 2>nul & del \"%~f0\"\r\n"
}
