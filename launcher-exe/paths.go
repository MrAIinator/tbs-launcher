package main

import (
	"os"
	"path/filepath"
	"runtime"
)

func dataDir() string {
	if runtime.GOOS == "windows" {
		if app := os.Getenv("APPDATA"); app != "" {
			return filepath.Join(app, "TBSLauncher")
		}
	}
	if app := os.Getenv("APPDATA"); app != "" && runtime.GOOS == "windows" {
		return filepath.Join(app, "TBSLauncher")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(home, "AppData", "Roaming", "TBSLauncher")
	}
	return filepath.Join(home, ".tbs-launcher")
}

func gameDir() string    { return filepath.Join(dataDir(), "game") }
func cacheDir() string   { return filepath.Join(dataDir(), "cache") }
func runtimeDir() string { return filepath.Join(dataDir(), "runtime") }
