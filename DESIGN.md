# tswipoexp design

This is tswipoexp, an experiment at building a "Portable" Windows
Tailscale client, where "Portable" in Windows vernacular doesn't mean
that you can cross-compile it for NetBSD, but instead means it's still
only Windows but:

1) no installation required (no setup wizard, no admin rights, DLLs, services, registry)
2) self-contained state next to the binary

So you can run it from a USB stick, close it, eject the USB stick,
move it to another computer, and pick up where you left off.

This project is an experiment to build a Tailscale client that behaves like that,
which is pretty much the opposite of the official Tailscale client in every regard.

To achieve that, it runs all the networking in userspace (tsnet with
gVisor netstack) and runs a proxy for browsers and other apps to use
to get out to the tailnet.

It contains two binaries:

* tswipoexp.exe: effectively tailscaled, but also a GUI
* tspo.exe: effectively tailscale.exe, the optional CLI binary that talks to tswipoexp.exe

The main binary lives here in the root.

tspo.exe is in ./cmd/tspo.

## Decisions

These were decided up front. Later sections may refine them.

### GUI

Fyne (https://fyne.io/). It produces a single static exe with no DLL
or runtime dependencies, which is what portable requires. Binary size
is not a concern.

The GUI is a full window, not just a tray icon: peer list, login
button, auth key entry, profile picker, proxy status, settings.

### Proxy

Both SOCKS5 and HTTP CONNECT, on the same localhost port, the same
way tailscaled's userspace-networking mode does it (net/proxymux
splits the two protocols on one listener).

While tswipoexp.exe is running, it registers the proxy with the
current Windows user session so apps pick it up automatically, and
unregisters it on exit. How exactly (per-user WinINet proxy settings
in HKCU, environment variables, or both) is to be determined; it must
not require admin rights.

### Exit nodes

Supported, since userspace netstack handles them fine, but this comes
after the basics work.

### Inbound

Inbound connections are supported. Tailscale SSH is supported as an
opt-in feature; see how tailcat (~/src/github.com/tailscale/tailcat)
runs an SSH server on Windows without being a service. There is also
a Shields Up option to block all incoming traffic.

Inbound is lower priority than outbound via the proxy.

### Profiles, identity, and state

State lives next to the binary and supports multiple profiles, one
per directory. With the USB stick at D:, the layout is:

    D:\tswipoexp.exe
    D:\tswipoexp-state\
    D:\tswipoexp-state\active-profile.txt   => "Work"
    D:\tswipoexp-state\Work\...             (any files tsnet needs)
    D:\tswipoexp-state\Home\...

The GUI lists profiles by enumerating the state directory and lets
the user switch between them. active-profile.txt names the profile
to use at startup.

A profile is one node identity that roams with the stick, so the same
node appears from whichever computer it's plugged into.

### Hostname

Defaults to "tswipoexp". The GUI offers changing it or syncing it to
the system's computer name.

### CLI to daemon transport

Whatever is easiest: a Windows named pipe or a loopback TCP address
plus credential written to disk in the state directory. tsnet's
Loopback method already serves the LocalAPI with a random credential
over loopback TCP.

tspo uses the real tailscale.com/cmd/tailscale/cli package so it gets
the full CLI for free.

### Login

Both interactive browser login and auth key mode, with a text entry
in the GUI for the auth key.

### Single instance

Only one instance of tswipoexp.exe may run at a time.

### Build

Cross-compiled from Linux (with Docker if needed for the cgo toolchain
that Fyne requires). Testing is done on a Windows laptop reachable
via tailcat; its address is in ~/keys/win-surface.

## Decisions taken while implementing

These were made without discussion to keep moving. Revisit any of them.

### Layout of the code

* Root package: the GUI binary. tswipoexp.go holds main and the App
  type; backend.go wraps one profile's tsnet node; profiles.go
  handles the state directory; ui.go is the Fyne window; debug.go is
  the debug endpoint; localapi.go bridges the LocalAPI to the named
  pipe.
* cmd/tspo: the CLI. It is tailscale.com/cmd/tailscale/cli with a
  --socket flag pointing at the named pipe prepended to the args.
* cmd/devtarget: a tsnet node serving an HTTP echo page on the test
  tailnet, used as a fetch target during development. Not shipped.

### Per-profile config

Each profile directory has a tswipoexp.json with the settings that
tsnet has no concept of or that are needed before it starts:
Hostname, SyncHostname, ProxyAddr, RegisterProxy. Everything else
(shields up, exit node, the node key) lives in tsnet's own state
files in the same directory. Writes are atomic (temp file plus
rename) since the directory may be on a USB stick.

A profile named "Default" is created when the state directory has
none.

### Proxy

Fixed default address 127.0.0.1:1055, per-profile configurable, no
authentication (loopback only). The dialer fails fast with a clear
error while the node isn't in the Running state rather than blocking
the browser.

### LocalAPI for the CLI

tsnet only exposes the LocalAPI in-process or over a loopback TCP
port that requires a random credential plus a Sec-Tailscale header.
tswipoexp serves a fixed named pipe, \\.\pipe\tswipoexp, and reverse
proxies it to that loopback port, adding the credential and header.
The real tailscale CLI can then talk to it via --socket, unchanged.

The pipe ACL grants access only to the current user and SYSTEM. It
can't use tailscaled's safesocket package because that sets Builtin
Administrators as the pipe owner, which fails without admin rights.

Creating the pipe is also the single-instance lock: a second copy
fails to create it and exits with a message. The lock is taken before
any profile directory is touched.

### Logging

Each profile has a tswipoexp.log next to its tsnet state. tsnet's
own logtail configuration and upload behavior is left at its default,
which means logs are uploaded to Tailscale's log service like any
tsnet app. Open question: whether a portable client should do that.

### Debug endpoint

--debug-addr=127.0.0.1:PORT (loopback only, off by default) serves:
/debug/state (JSON summary), /debug/status (raw ipnstate.Status),
/debug/tree (named widgets and their text, options, enabled state),
POST /debug/tap?name=, POST /debug/set?name=&text=,
/debug/screenshot (PNG of the Fyne canvas, works with a locked
desktop), /debug/log (recent log lines), POST /debug/quit.

### Auth key login from the GUI

Entering a key calls LocalAPI Start with the key and current prefs,
then StartLoginInteractive, which is what "tailscale up --authkey"
does. Browser login shows the URL from the IPN bus behind a button
rather than opening the browser unprompted.

## Open questions

* Should logtail uploads be disabled for a portable client?
* Windows Firewall prompts when tsnet first binds UDP. Inbound direct
  connections need the user to allow it (admin), outbound works
  regardless. Do we warn in the GUI?
* If tswipoexp crashes while the proxy is registered with the user
  session, the registration lingers. Plan: save the previous settings
  in the state directory and restore them at next start.

## Plan

1. Hello World Fyne GUI cross-compiled from Linux and running on the Windows laptop.
2. tsnet in the GUI: login, profile directories, status, peer list.
3. SOCKS5 + HTTP proxy on localhost, registered with the Windows user session.
4. tspo CLI over the LocalAPI.
5. Hostname setting, Shields Up, auth key login.
6. Exit nodes.
7. Inbound: Tailscale SSH (opt-in).
