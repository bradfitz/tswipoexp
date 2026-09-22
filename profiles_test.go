package main

import (
	"testing"
)

func TestProfiles(t *testing.T) {
	p := &Profiles{Root: t.TempDir() + "/state"}
	names, err := p.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != defaultProfileName {
		t.Fatalf("List = %q; want just %q", names, defaultProfileName)
	}
	if err := p.Create("Work"); err != nil {
		t.Fatal(err)
	}
	if err := p.Create("Work"); err == nil {
		t.Fatal("duplicate Create succeeded")
	}
	if err := p.Create("bad/name"); err == nil {
		t.Fatal("Create with slash succeeded")
	}
	active, err := p.Active()
	if err != nil {
		t.Fatal(err)
	}
	if active != defaultProfileName {
		t.Errorf("Active = %q; want %q", active, defaultProfileName)
	}
	if err := p.SetActive("Work"); err != nil {
		t.Fatal(err)
	}
	if active, _ := p.Active(); active != "Work" {
		t.Errorf("Active = %q; want Work", active)
	}
	c, err := p.LoadConfig("Work")
	if err != nil {
		t.Fatal(err)
	}
	if c.hostname() != defaultHostname || c.proxyAddr() != defaultProxyAddr || !c.registerProxy() {
		t.Errorf("zero config defaults wrong: %+v", c)
	}
	c.Hostname = "laptop"
	off := false
	c.RegisterProxy = &off
	if err := p.SaveConfig("Work", c); err != nil {
		t.Fatal(err)
	}
	c2, err := p.LoadConfig("Work")
	if err != nil {
		t.Fatal(err)
	}
	if c2.hostname() != "laptop" || c2.registerProxy() {
		t.Errorf("reloaded config wrong: %+v", c2)
	}
}
