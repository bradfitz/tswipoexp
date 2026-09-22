package main

import (
	"strings"
	"testing"
)

func TestRestoreRegFile(t *testing.T) {
	s := &savedProxySettings{
		ProxyFlags:    9, // direct + auto-detect, like a default Windows install
		ProxyOverride: `<local>;*.example.com`,
		Env:           map[string]string{"HTTP_PROXY": `http://old:3128`},
	}
	got := restoreRegFile(s, []byte{0x46, 0, 0, 0, 9})
	for _, want := range []string{
		"Windows Registry Editor Version 5.00",
		`"ProxyEnable"=dword:00000000`,
		`"ProxyServer"=-`,
		`"ProxyOverride"="<local>;*.example.com"`,
		`"AutoConfigURL"=-`,
		`"DefaultConnectionSettings"=hex:46,00,00,00,09`,
		`"HTTP_PROXY"="http://old:3128"`,
		`"HTTPS_PROXY"=-`,
		`"NO_PROXY"=-`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	s.ProxyFlags = proxyTypeDirect | proxyTypeProxy
	s.ProxyServer = `http=1.2.3.4:8080`
	got = restoreRegFile(s, nil)
	if !strings.Contains(got, `"ProxyEnable"=dword:00000001`) || !strings.Contains(got, `"ProxyServer"="http=1.2.3.4:8080"`) {
		t.Errorf("proxy-on case wrong:\n%s", got)
	}
	if strings.Contains(got, "DefaultConnectionSettings") {
		t.Error("nil blob should omit the Connections key")
	}
}

func TestRestoreCmdFile(t *testing.T) {
	got := restoreCmdFile("restore.reg")
	if !strings.Contains(got, `reg import "%~dp0restore.reg"`) || !strings.Contains(got, `del "%~f0"`) {
		t.Errorf("unexpected cmd:\n%s", got)
	}
}
