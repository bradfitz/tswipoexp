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

## Plan

1. Hello World Fyne GUI cross-compiled from Linux and running on the Windows laptop.
2. tsnet in the GUI: login, profile directories, status, peer list.
3. SOCKS5 + HTTP proxy on localhost, registered with the Windows user session.
4. tspo CLI over the LocalAPI.
5. Hostname setting, Shields Up, auth key login.
6. Exit nodes.
7. Inbound: Tailscale SSH (opt-in).
