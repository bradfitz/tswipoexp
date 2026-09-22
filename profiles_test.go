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

func TestValidProfileName(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"Work", true},
		{"Home 2", true},
		{"", false},
		{" lead", false},
		{".hidden", false},
		{`a\b`, false},
		{"a/b", false},
		{"a:b", false},
		{"a*b", false},
	} {
		if got := validProfileName(tc.name); got != tc.ok {
			t.Errorf("validProfileName(%q) = %v; want %v", tc.name, got, tc.ok)
		}
	}
}

func TestGUIState(t *testing.T) {
	root := t.TempDir()
	if s := loadGUIState(root); s.WindowWidth != 0 {
		t.Fatalf("missing file yielded %+v", s)
	}
	if err := saveGUIState(root, guiState{WindowWidth: 640, WindowHeight: 480}); err != nil {
		t.Fatal(err)
	}
	s := loadGUIState(root)
	if s.WindowWidth != 640 || s.WindowHeight != 480 {
		t.Errorf("reloaded %+v", s)
	}
}

func TestSSHMode(t *testing.T) {
	for _, tc := range []struct {
		cfg  Config
		want string
	}{
		{Config{}, sshOff},
		{Config{SSH: true}, sshSameUser},
		{Config{SSHMode: sshOff, SSH: true}, sshOff},
		{Config{SSHMode: sshAllUsers}, sshAllUsers},
		{Config{SSHMode: "bogus"}, sshOff},
	} {
		if got := tc.cfg.sshMode(); got != tc.want {
			t.Errorf("%+v: sshMode = %q; want %q", tc.cfg, got, tc.want)
		}
	}
}
