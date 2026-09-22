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

CC_WIN ?= x86_64-w64-mingw32-gcc
CXX_WIN ?= x86_64-w64-mingw32-g++

GOENV_WIN = CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=$(CC_WIN) CXX=$(CXX_WIN)

# -H windowsgui keeps a console window from appearing next to the GUI.
LDFLAGS_GUI = -H windowsgui -s -w
LDFLAGS_CLI = -s -w

.PHONY: all build push run push-run ssh shot clean

all: build

build: dist/tswipoexp.exe dist/tspo.exe

dist/tswipoexp.exe: $(wildcard *.go) go.mod go.sum
	$(GOENV_WIN) go build -ldflags="$(LDFLAGS_GUI)" -o $@ .

dist/tspo.exe: $(wildcard cmd/tspo/*.go) go.mod go.sum
	@if [ -d cmd/tspo ]; then $(GOENV_WIN) go build -ldflags="$(LDFLAGS_CLI)" -o $@ ./cmd/tspo; else echo "cmd/tspo not yet present; skipping"; fi

# push copies the built binaries to the Windows box.
push: build
	@tailcat ssh $(WIN_ADDR) 'New-Item -ItemType Directory -Force -Path $(WIN_DIR) | Out-Null'
	@for f in dist/*.exe; do tailcat cp $$f $(WIN_ADDR):$(WIN_SFTP_DIR)/$$(basename $$f); done

# run starts the GUI on the Windows box, detached from the SSH session.
run:
	@tailcat ssh $(WIN_ADDR) 'Stop-Process -Name tswipoexp -Force -ErrorAction SilentlyContinue; Start-Process -FilePath $(WIN_DIR)/tswipoexp.exe -WorkingDirectory $(WIN_DIR)'

push-run: push run

# ssh opens an interactive PowerShell on the Windows box.
ssh:
	@tailcat ssh $(WIN_ADDR)

clean:
	rm -rf dist

# shot captures the Windows box's primary screen to shot.png here.
shot:
	@tailcat ssh $(WIN_ADDR) 'Add-Type -AssemblyName System.Windows.Forms,System.Drawing; $$b=[System.Windows.Forms.Screen]::PrimaryScreen.Bounds; $$bmp=New-Object System.Drawing.Bitmap $$b.Width,$$b.Height; $$g=[System.Drawing.Graphics]::FromImage($$bmp); $$g.CopyFromScreen($$b.Location,[System.Drawing.Point]::Empty,$$b.Size); $$bmp.Save("$(WIN_DIR)/shot.png")'
	@tailcat cp $(WIN_ADDR):$(WIN_SFTP_DIR)/shot.png shot.png
