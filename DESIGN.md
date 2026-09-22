This is tswipoexp, an experiment at building a "Portable" Windows
Tailscale client, where "Portable" in Windows vernacular doesn't mean
that you can cross-compile it for NetBSD, but instead means it's still
only Windows but:

1) no installation required (no setup wixard, no admin rights, DLLs, services, registry)
2) self-contained state next to the binary

So you can run it from a USB stick, close it, eject the USB stick,
move it to another computer, and pick up where you left off.

This project is an experiment to build a Tailscale client that behaves like that,
which is pretty much the opposite of the official Tailscale client in very regard.

To achieve that, it runs all the networking in userspace with runs a
proxy for browsers and other apps to use to get out to the tailnet.

It contains two binaries:

* tswipoexp.exe: effectively tailscaled, but also a GUI
* tspo.exe: effectively tailscale.exe, the optional CLI binary that talks to tswipoexp.exe

The binary binary lives here in the root.

tspo.exe is in ./cmd/tspo.
