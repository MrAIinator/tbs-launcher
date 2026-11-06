//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"
)

func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// CREATE_NO_WINDOW only kills the console. SW_HIDE was hiding the
		// NeoForge early window and the game died a few seconds into loading.
		CreationFlags: 0x08000000 | 0x00000200,
	}
}

func alert(text string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	proc := user32.NewProc("MessageBoxW")
	title, _ := syscall.UTF16PtrFromString("TBS Launcher")
	body, _ := syscall.UTF16PtrFromString(text)
	_, _, _ = proc.Call(0, uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(title)), 0x00000010)
}

func findBrowser() string {
	local := os.Getenv("LOCALAPPDATA")
	pf := os.Getenv("ProgramFiles")
	pf86 := os.Getenv("ProgramFiles(x86)")
	if pf == "" {
		pf = os.Getenv("PROGRAMFILES")
	}
	if pf86 == "" {
		pf86 = os.Getenv("PROGRAMFILES(X86)")
	}
	candidates := []string{
		filepath.Join(local, "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(pf86, "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(pf, "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(local, "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(pf, "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(pf86, "Google", "Chrome", "Application", "chrome.exe"),
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

func openWindow(url string) error {
	browser := findBrowser()
	if browser == "" {
		return errNoBrowser
	}
	dir := filepath.Join(dataDir(), "webview")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cmd := exec.Command(browser,
		"--app="+url,
		"--user-data-dir="+dir,
		"--window-size=1200,760",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
	)
	return cmd.Start()
}

var errNoBrowser = exec.ErrNotFound

func openExternal(rawURL string) {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	hideConsole(cmd)
	_ = cmd.Start()
}

func execCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
