package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	ssh "github.com/tailscale/gliderssh"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// This file's ConPTY handling is adapted from tailcat's
// tailcat_ssh_windows.go (BSD-3-Clause, Tailscale Inc).

// newSessionCommand returns an unstarted PowerShell command:
// interactive when rawCmd is empty, otherwise running rawCmd. The
// caller appends any client-provided environment.
func newSessionCommand(u *user.User, rawCmd string) *exec.Cmd {
	clearInheritedCtrlCIgnore()
	shell := powerShellPath()
	var args []string
	if rawCmd == "" {
		args = []string{shell, "-NoLogo"}
	} else {
		args = []string{shell, "-Command", rawCmd}
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = u.HomeDir
	// Windows processes need most of the parent environment
	// (SystemRoot, TEMP, and friends), so the session inherits it.
	cmd.Env = os.Environ()
	return cmd
}

// powerShellPath returns the PowerShell to use, preferring the shell
// configured for OpenSSH, then PowerShell 7, then Windows PowerShell.
func powerShellPath() string {
	if key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\OpenSSH`, registry.QUERY_VALUE|registry.WOW64_64KEY); err == nil {
		shell, _, _ := key.GetStringValue("DefaultShell")
		key.Close()
		name := strings.ToLower(filepath.Base(shell))
		if name == "pwsh.exe" || name == "powershell.exe" {
			if p, err := exec.LookPath(shell); err == nil {
				return p
			}
		}
	}
	if p, err := exec.LookPath("pwsh.exe"); err == nil {
		return p
	}
	pwsh := filepath.Join(os.Getenv("ProgramFiles"), "PowerShell", "7", "pwsh.exe")
	if p, err := exec.LookPath(pwsh); err == nil {
		return p
	}
	if p, err := exec.LookPath("powershell.exe"); err == nil {
		return p
	}
	return filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
}

var (
	procCreatePseudoConsole       = kernel32.NewProc("CreatePseudoConsole")
	procUpdateProcThreadAttribute = kernel32.NewProc("UpdateProcThreadAttribute")
	procSetConsoleCtrlHandler     = kernel32.NewProc("SetConsoleCtrlHandler")
)

// clearInheritedCtrlCIgnore clears this process's "ignore Ctrl-C"
// console flag, which session children would inherit and which would
// make ^C in an interactive session get silently dropped.
var clearInheritedCtrlCIgnore = sync.OnceFunc(func() {
	procSetConsoleCtrlHandler.Call(0, 0)
})

// conPTYAvailable reports whether this Windows has the pseudoconsole
// API (Windows 10 1809 or later).
var conPTYAvailable = sync.OnceValue(func() bool {
	return procCreatePseudoConsole.Find() == nil
})

// runWithPTY runs cmd attached to a Windows pseudoconsole. The
// exec.Cmd only carries Args, Env, and Dir; the process is created
// directly since pseudoconsole attachment needs CreateProcess.
func runWithPTY(sess ssh.Session, cmd *exec.Cmd, ptyReq ssh.Pty, winCh <-chan ssh.Window) {
	if !conPTYAvailable() {
		fmt.Fprintf(sess.Stderr(), "tswipoexp: no ConPTY on this Windows version; running without a PTY\r\n")
		runWithPipes(sess, cmd)
		return
	}
	if ptyReq.Term != "" {
		cmd.Env = append(cmd.Env, "TERM="+ptyReq.Term)
	}
	cp, err := startConPTY(cmd, ptyReq.Window.Width, ptyReq.Window.Height)
	if err != nil {
		fmt.Fprintf(sess.Stderr(), "conpty: %v\r\n", err)
		sess.Exit(1)
		return
	}
	defer cp.Close()

	go io.Copy(cp.inWrite, sess) // stdin

	// winCh is closed by gliderssh when the session channel shuts
	// down, possibly after this function returns; Resize is a no-op
	// once the pseudoconsole is closed.
	go func() {
		for win := range winCh {
			cp.Resize(win.Width, win.Height)
		}
	}()

	// The output reader must be running before CloseConsole below,
	// which can block until pending output has been consumed.
	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		io.Copy(sess, cp.outRead)
	}()

	code, err := cp.WaitExitCode()
	sshDebugf("ssh: pty process exited code=%d err=%v", code, err)
	// Closing the pseudoconsole makes conhost release the output
	// pipe, which is what lets the reader see EOF. Both steps have
	// been seen to stall for some PowerShell sessions (for example
	// ones that queried the console size), so neither is allowed to
	// hold the session open indefinitely: the process is gone and
	// its output has been read, so the session ends regardless.
	closed := make(chan struct{})
	go func() {
		cp.CloseConsole()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		sshDebugf("ssh: ClosePseudoConsole did not return within 3s; continuing")
	}
	select {
	case <-outDone:
	case <-time.After(2 * time.Second):
		sshDebugf("ssh: pty output did not reach EOF within 2s; continuing")
	}

	if err != nil {
		fmt.Fprintf(sess.Stderr(), "wait: %v\r\n", err)
		sess.Exit(1)
		return
	}
	sess.Exit(code)
}

// conPTY is a pseudoconsole with one process attached.
type conPTY struct {
	inWrite *os.File // the process reads its input from here
	outRead *os.File // the process's output is read from here
	proc    windows.Handle

	mu     sync.Mutex // guards hpc and closed: Resize races CloseConsole
	hpc    windows.Handle
	closed bool
}

// startConPTY creates a pseudoconsole and starts cmd attached to it.
func startConPTY(cmd *exec.Cmd, width, height int) (*conPTY, error) {
	// CreatePseudoConsole rejects empty dimensions, which clients
	// without a real terminal can send.
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	inRead, inWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		inRead.Close()
		inWrite.Close()
		return nil, err
	}
	var hpc windows.Handle
	err = windows.CreatePseudoConsole(
		windows.Coord{X: int16(width), Y: int16(height)},
		windows.Handle(inRead.Fd()),
		windows.Handle(outWrite.Fd()),
		0, &hpc)
	// The pseudoconsole duplicates the handles it needs.
	inRead.Close()
	outWrite.Close()
	if err != nil {
		inWrite.Close()
		outRead.Close()
		return nil, fmt.Errorf("CreatePseudoConsole: %w", err)
	}
	cleanup := func() {
		windows.ClosePseudoConsole(hpc)
		inWrite.Close()
		outRead.Close()
	}

	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		cleanup()
		return nil, err
	}
	defer attrs.Delete()
	if err := updatePseudoConsoleAttr(attrs.List(), hpc); err != nil {
		cleanup()
		return nil, err
	}
	cmdLine, err := windows.UTF16FromString(windows.ComposeCommandLine(cmd.Args))
	if err != nil {
		cleanup()
		return nil, err
	}
	var dir *uint16
	if cmd.Dir != "" {
		dir, err = windows.UTF16PtrFromString(cmd.Dir)
		if err != nil {
			cleanup()
			return nil, err
		}
	}
	env, err := envBlock(cmd.Env)
	if err != nil {
		cleanup()
		return nil, err
	}
	si := &windows.StartupInfoEx{
		// STARTF_USESTDHANDLES with NULL std handles keeps the
		// child from inheriting this process's console handles.
		StartupInfo: windows.StartupInfo{
			Cb:    uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Flags: windows.STARTF_USESTDHANDLES,
		},
		ProcThreadAttributeList: attrs.List(),
	}
	var pi windows.ProcessInformation
	err = windows.CreateProcess(nil, &cmdLine[0], nil, nil, false,
		windows.CREATE_UNICODE_ENVIRONMENT|windows.EXTENDED_STARTUPINFO_PRESENT,
		env, dir, &si.StartupInfo, &pi)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("CreateProcess: %w", err)
	}
	windows.CloseHandle(pi.Thread)
	return &conPTY{inWrite: inWrite, outRead: outRead, proc: pi.Process, hpc: hpc}, nil
}

// updatePseudoConsoleAttr sets the pseudoconsole attribute. It calls
// UpdateProcThreadAttribute directly because the attribute's value is
// the handle itself, not a pointer, which the x/sys wrapper's
// unsafe.Pointer argument can't express without upsetting vet.
func updatePseudoConsoleAttr(list *windows.ProcThreadAttributeList, hpc windows.Handle) error {
	r1, _, e1 := procUpdateProcThreadAttribute.Call(
		uintptr(unsafe.Pointer(list)),
		0,
		windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		uintptr(hpc),
		unsafe.Sizeof(hpc),
		0, 0)
	if r1 == 0 {
		return e1
	}
	return nil
}

// Resize changes the pseudoconsole dimensions.
func (c *conPTY) Resize(width, height int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	return windows.ResizePseudoConsole(c.hpc, windows.Coord{X: int16(width), Y: int16(height)})
}

// WaitExitCode waits for the process and returns its exit code.
func (c *conPTY) WaitExitCode() (int, error) {
	if _, err := windows.WaitForSingleObject(c.proc, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(c.proc, &code); err != nil {
		return 0, err
	}
	return int(code), nil
}

// CloseConsole closes the pseudoconsole but not the pipes or process.
func (c *conPTY) CloseConsole() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		windows.ClosePseudoConsole(c.hpc)
		c.closed = true
	}
}

// Close releases everything.
func (c *conPTY) Close() {
	c.CloseConsole()
	c.inWrite.Close()
	c.outRead.Close()
	windows.CloseHandle(c.proc)
}

// envBlock converts key=value pairs to the UTF-16, NUL-separated,
// double-NUL-terminated block CreateProcess expects. Keys are
// case-insensitive on Windows and later entries replace earlier ones.
func envBlock(env []string) (*uint16, error) {
	seen := make(map[string]int)
	var list []string
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		uk := strings.ToUpper(k)
		if i, dup := seen[uk]; dup {
			list[i] = kv
		} else {
			seen[uk] = len(list)
			list = append(list, kv)
		}
	}
	var block []uint16
	for _, kv := range list {
		u, err := windows.UTF16FromString(kv)
		if err != nil {
			return nil, err
		}
		block = append(block, u...)
	}
	block = append(block, 0)
	return &block[0], nil
}
