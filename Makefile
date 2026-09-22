# Cross-compiles tswipoexp for Windows from Linux and pushes it to a
# Windows test box over tailcat.
#
# The tailcat address of the test box is read from a file outside the
# repo so it never gets committed. Override with:
#   make push WIN_ADDR_FILE=/path/to/file
# or
#   make push WIN_ADDR=tc....

WIN_ADDR_FILE ?= $(HOME)/keys/win-surface
WIN_ADDR ?= $(shell cat $(WIN_ADDR_FILE))
WIN_DIR ?= C:/tswipoexp
# The tailcat SFTP server on the Windows box is rooted at C:\, so copy
# destinations are relative to that root.
WIN_SFTP_DIR ?= tswipoexp
# DEBUG_ADDR is where the pushed GUI serves its debug endpoint; empty disables it.
DEBUG_ADDR ?= 127.0.0.1:8181

CC_WIN ?= x86_64-w64-mingw32-gcc
CXX_WIN ?= x86_64-w64-mingw32-g++

GOENV_WIN = CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=$(CC_WIN) CXX=$(CXX_WIN)

# -H windowsgui keeps a console window from appearing next to the GUI.
LDFLAGS_GUI = -H windowsgui -s -w
LDFLAGS_CLI = -s -w

.PHONY: all build push stop run push-run ssh shot dbg edge clean

all: build

build: dist/tswipoexp.exe dist/tspo.exe

dist/tswipoexp.exe: $(wildcard *.go) go.mod go.sum
	$(GOENV_WIN) go build -ldflags="$(LDFLAGS_GUI)" -o $@ .

dist/tspo.exe: $(wildcard cmd/tspo/*.go) go.mod go.sum
	@if [ -d cmd/tspo ]; then $(GOENV_WIN) go build -ldflags="$(LDFLAGS_CLI)" -o $@ ./cmd/tspo; else echo "cmd/tspo not yet present; skipping"; fi

# push copies the built binaries to the Windows box, first stopping
# any running instance (gracefully via the debug endpoint so it
# restores the system proxy settings, then forcibly) since Windows
# won't overwrite a running executable.
push: build stop
	@tailcat ssh $(WIN_ADDR) 'New-Item -ItemType Directory -Force -Path $(WIN_DIR) | Out-Null'
	@for f in dist/*.exe tools/*.cmd; do tailcat cp $$f $(WIN_ADDR):$(WIN_SFTP_DIR)/$$(basename $$f); done

# stop quits the GUI on the Windows box if it's running.
stop:
	@tailcat ssh $(WIN_ADDR) 'if (Get-Process tswipoexp -ErrorAction SilentlyContinue) { curl.exe -s -m 3 -X POST http://$(DEBUG_ADDR)/debug/quit 2>&1 | Out-Null; Start-Sleep 2; Get-Process tswipoexp -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue }; exit 0'

# run starts the GUI on the Windows box, detached from the SSH session.
run: stop
	@tailcat ssh $(WIN_ADDR) 'Start-Process -FilePath $(WIN_DIR)/tswipoexp.exe -WorkingDirectory $(WIN_DIR) -ArgumentList "--debug-addr=$(DEBUG_ADDR)"'

push-run: push run

# dbg fetches a debug endpoint path from the running GUI, e.g.
#   make dbg P=/debug/state
#   make dbg P=/debug/tap M=POST Q=name=login
P ?= /debug/state
M ?= GET
Q ?=
dbg:
	@tailcat ssh $(WIN_ADDR) 'curl.exe -s -m 20 -X $(M) "http://$(DEBUG_ADDR)$(P)?$(Q)"; exit 0'

# edge fetches URL with headless Edge on the Windows box, which
# honors the Windows system proxy, and prints matching lines.
#   make edge URL=http://tswipoexp-target/
URL ?= http://tswipoexp-target/
edge:
	@tailcat ssh $(WIN_ADDR) '& $(WIN_DIR)/edge-dump.cmd $(URL); Get-Content $(WIN_DIR)/edge-out.txt | Select-String -Pattern "hello from|peer:|ERR_|<title>" | Select -First 3; exit 0'

# ssh opens an interactive PowerShell on the Windows box.
ssh:
	@tailcat ssh $(WIN_ADDR)

clean:
	rm -rf dist

# shot captures the Windows box's primary screen to shot.png here. The
# capture is made DPI aware first; otherwise it's a physical-pixel crop
# of the top-left of a scaled display, which looks like cut-off windows.
shot:
	@tailcat ssh $(WIN_ADDR) '$$q=[char]34; Add-Type -Name DPI -Namespace Win -MemberDefinition ("[DllImport(" + $$q + "user32.dll" + $$q + ")] public static extern bool SetProcessDPIAware();"); [Win.DPI]::SetProcessDPIAware() | Out-Null; Add-Type -AssemblyName System.Windows.Forms,System.Drawing; $$b=[System.Windows.Forms.Screen]::PrimaryScreen.Bounds; $$bmp=New-Object System.Drawing.Bitmap $$b.Width,$$b.Height; $$g=[System.Drawing.Graphics]::FromImage($$bmp); $$g.CopyFromScreen($$b.Location,[System.Drawing.Point]::Empty,$$b.Size); $$bmp.Save("$(WIN_DIR)/shot.png")'
	@tailcat cp $(WIN_ADDR):$(WIN_SFTP_DIR)/shot.png shot.png
