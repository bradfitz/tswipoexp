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

### Registering the proxy with Windows

While a profile is running and its RegisterProxy setting is on (the
default), tswipoexp makes itself the current user's proxy:

* WinINet per-connection settings via InternetSetOption with
  INTERNET_OPTION_PER_CONNECTION_OPTION: flags DIRECT|PROXY, server
  "http=ADDR;https=ADDR", bypass "localhost;127.*;[::1]". The usual
  "<local>" bypass is not used because it means "any hostname without
  a dot", which would route short MagicDNS names around the proxy
  (this bit us: Edge got ERR_NAME_NOT_RESOLVED for short names while
  FQDNs worked). Writing the legacy
  ProxyEnable/ProxyServer registry values is not enough: WinHTTP
  users (.NET, PowerShell) and Chromium browsers read the
  per-connection blob, and Edge failed with ERR_NAME_NOT_RESOLVED
  until this was switched. No socks= rule, since Chromium treats it
  as SOCKS4.
* Per-user environment variables HTTP_PROXY, HTTPS_PROXY, and
  NO_PROXY in HKCU\Environment plus a WM_SETTINGCHANGE broadcast, so
  newly launched command line tools pick them up.

The previous settings are saved to %LOCALAPPDATA%\tswipoexp\
proxy-restore.json and restored at exit, or at the next start on the
same machine if the previous run died. That file is the one deliberate
piece of state kept off the stick: it describes this machine, not the
profile. A stick pulled from machine A and started on machine B can't
repair A; A's browser is broken until tswipoexp runs there again or
the user fixes the proxy setting by hand. Open question below.

While the node is not Running, the proxy dials directly instead of
failing, so the user's browsing keeps working with the proxy still
registered; only tailnet names fail.

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

### Exit nodes

The GUI has an exit node dropdown listing peers with the exit node
option, plus None, applied via EditPrefs. Tested end to end: with
the exit node selected, curl through the proxy and Edge through the
system proxy both egress from the exit node's public IP, and DNS
goes via the exit node's peer API resolver.

cmd/devtarget has an -exit-node flag that advertises the routes
(approved by Brad in the admin console). A plain tsnet node can't act
as an exit node because tsnet resets any TCP flow no listener claims;
devtarget works around it with RegisterFallbackTCPHandler forwarding
non-tailnet destinations. That's a tsnet limitation, not a tswipoexp
one, and only matters for the test target.

### SSH

Tailscale's own SSH server doesn't build on Windows, so tswipoexp
follows tailcat's approach instead. A per-profile opt-in (off by
default) runs a gliderssh server on the node's tailnet addresses at
port 22 via tsnet's Listen. Authorization is by tunnel identity:
WhoIs on the peer address must return the same tailnet login as this
node's owner; there is no SSH-level client authentication and tagged
or other users' devices are refused. Sessions run PowerShell as the
current Windows user, over ConPTY when the client asks for a PTY
(code adapted from tailcat). The ed25519 host key lives in the profile
directory so it roams with the profile. No SFTP, no port forwarding.

Tailscale SSH ACL rules from the tailnet policy are not consulted;
that's an open question below. cmd/devssh is a tsnet SSH client for
testing this from Linux.

## Open questions

* SSH authorization ignores the tailnet's SSH ACL rules and uses
  "same user" only. Options: honor the SSH rules from the netmap (not
  exposed by tsnet today), or add an allowlist of users/tags in the
  profile config.

* Should logtail uploads be disabled for a portable client?
* Windows Firewall prompts when tsnet first binds UDP. Inbound direct
  connections need the user to allow it (admin), outbound works
  regardless. Do we warn in the GUI?
* Pulling the stick while running leaves the machine's proxy pointed
  at a dead port until tswipoexp runs there again. Could be mitigated
  by a tiny watchdog or by using a PAC URL served by the proxy, which
  browsers ignore when unreachable.
* PAC versus static proxy: a PAC file could send only tailnet
  destinations through the proxy and leave the rest alone. Static was
  chosen for now because it's simpler and gives exit node semantics
  for free.
* Testing the browser path on the laptop uses headless Edge via
  tools/edge-dump.cmd; Edge produces no output when run directly from
  PowerShell.

## Plan

Done:

1. Hello World Fyne GUI cross-compiled from Linux and running on the Windows laptop.
2. tsnet in the GUI: login, profile directories, status, peer list.
3. SOCKS5 + HTTP proxy on localhost, registered with the Windows user session.
4. tspo CLI over the LocalAPI.
5. Hostname setting, Shields Up, auth key login.

Next:

6. Exit nodes: picker in the GUI (done and tested).
7. Inbound: SSH (opt-in), done.
8. Polish: browser login flow verified end to end, profile switching
   verified, tray icon, remembering window size.
