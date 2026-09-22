package main

import (
	"strings"
	"testing"
)

func TestPACScript(t *testing.T) {
	in := pacInputs{ProxyAddr: "127.0.0.1:1055", DNSSuffix: "example.ts.net", Routes: []string{"192.168.5.0/24", "10.0.0.0/8"}}
	got := pacScript(in)
	for _, want := range []string{
		`var P = "PROXY 127.0.0.1:1055";`,
		`isInNet(host, "100.64.0.0", "255.192.0.0")`,
		`fd7a:115c:a1e0:`,
		`isPlainHostName(host)`,
		`dnsDomainIs(host, ".example.ts.net")`,
		`isInNet(host, "192.168.5.0", "255.255.255.0")`,
		`isInNet(host, "10.0.0.0", "255.0.0.0")`,
		"  return D;\n}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	all := pacScript(pacInputs{ProxyAddr: "127.0.0.1:1055", AllTraffic: true})
	if !strings.Contains(all, "  return P;\n}") || strings.Contains(all, "isPlainHostName") {
		t.Errorf("all-traffic script wrong:\n%s", all)
	}
	if !strings.Contains(all, `host == "localhost"`) {
		t.Error("all-traffic script should still bypass localhost")
	}
}

func TestV4Mask(t *testing.T) {
	for bits, want := range map[int]string{0: "0.0.0.0", 8: "255.0.0.0", 10: "255.192.0.0", 24: "255.255.255.0", 32: "255.255.255.255"} {
		if got := v4Mask(bits); got != want {
			t.Errorf("v4Mask(%d) = %s; want %s", bits, got, want)
		}
	}
}
