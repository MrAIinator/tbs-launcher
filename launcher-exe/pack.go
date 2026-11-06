package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type manifest struct {
	LauncherVersion string `json:"launcherVersion"`
	PackVersion     string `json:"packVersion"`
	PackName        string `json:"packName"`
	Minecraft       string `json:"minecraft"`
	NeoForge        string `json:"neoforge"`
	Server          struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	} `json:"server"`
	ManifestURL string `json:"manifestUrl"`
	PackURL     string `json:"packUrl"`
	LauncherURL string `json:"launcherUrl"`
	Notes       string `json:"notes"`
	BundledPack string `json:"bundledPackVersion"`
}

type modInfo struct {
	Name string  `json:"name"`
	Kind string  `json:"kind"`
	MB   float64 `json:"mb"`
}

type installedPack struct {
	PackVersion string            `json:"packVersion"`
	Minecraft   string            `json:"minecraft"`
	NeoForge    string            `json:"neoforge"`
	Name        string            `json:"name"`
	Files       map[string]string `json:"files"`
}

type mrFile struct {
	Path      string            `json:"path"`
	Downloads []string          `json:"downloads"`
	FileSize  int64             `json:"fileSize"`
	Hashes    map[string]string `json:"hashes"`
	Env       map[string]string `json:"env"`
}

type mrIndex struct {
	Name         string            `json:"name"`
	VersionID    string            `json:"versionId"`
	Dependencies map[string]string `json:"dependencies"`
	Files        []mrFile          `json:"files"`
}

func readBundledManifest() (manifest, error) {
	b, err := assets.ReadFile("manifest.json")
	if err != nil {
		return manifest{}, err
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return manifest{}, err
	}
	m.BundledPack = m.PackVersion
	return m, nil
}

func resolveManifest() manifest {
	bundled, err := readBundledManifest()
	if err != nil {
		return manifest{LauncherVersion: launcherVersion, Minecraft: "1.21.1", NeoForge: "21.1.249"}
	}
	if bundled.ManifestURL == "" {
		return bundled
	}
	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest(http.MethodGet, bundled.ManifestURL, nil)
	if err != nil {
		return bundled
	}
	req.Header.Set("User-Agent", "TBSLauncher/"+launcherVersion)
	resp, err := client.Do(req)
	if err != nil {
		return bundled
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return bundled
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return bundled
	}
	var remote manifest
	if json.Unmarshal(body, &remote) != nil || remote.PackVersion == "" {
		return bundled
	}
	remote.BundledPack = bundled.PackVersion
	if remote.ManifestURL == "" {
		remote.ManifestURL = bundled.ManifestURL
	}
	return remote
}

func installedPath() string { return filepath.Join(dataDir(), "installed.json") }

func readInstalled() *installedPack {
	b, err := os.ReadFile(installedPath())
	if err != nil {
		return nil
	}
	var p installedPack
	if json.Unmarshal(b, &p) != nil {
		return nil
	}
	return &p
}

func writeInstalled(p installedPack) error {
	if err := os.MkdirAll(dataDir(), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(installedPath(), b, 0o644)
}

func bundledMrpackPath() (string, error) {
	dest := filepath.Join(dataDir(), "bundled", "pack.mrpack")
	need := true
	if st, err := os.Stat(dest); err == nil && st.Size() == int64(len(bundledPack)) {
		if got, err := sha1File(dest); err == nil && got == sha1Bytes(bundledPack) {
			need = false
		}
	}
	if need {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(dest, bundledPack, 0o644); err != nil {
			return "", err
		}
	}
	return dest, nil
}

func mrpackPathFor(m manifest, hooks hook) (string, error) {
	bundledPath, err := bundledMrpackPath()
	if err != nil {
		return "", err
	}
	base := m.BundledPack
	if base == "" {
		base = m.PackVersion
	}
	if m.PackURL == "" || compareVer(base, m.PackVersion) >= 0 {
		return bundledPath, nil
	}
	dest := filepath.Join(cacheDir(), "pack-"+m.PackVersion+".mrpack")
	if _, err := os.Stat(dest); err != nil {
		logLine("качаю сборку " + m.PackVersion)
		if err := downloadFile(m.PackURL, dest, "", hooks, "сборка"); err != nil {
			logLine("не скачал новую сборку, ставлю ту, что внутри лаунчера: " + err.Error())
			return bundledPath, nil
		}
	}
	return dest, nil
}

func queueLauncherUpdate(m manifest) {
	if m.LauncherURL == "" || compareVer(launcherVersion, m.LauncherVersion) >= 0 {
		return
	}
	dest := filepath.Join(cacheDir(), "launcher-"+m.LauncherVersion+".bin")
	logLine("качаю лаунчер " + m.LauncherVersion + ", встанет после перезапуска")
	if err := downloadFile(m.LauncherURL, dest, "", hook{cancel: func() bool { return false }}, ""); err != nil {
		logLine("обновление лаунчера не скачалось: " + err.Error())
		return
	}
	next := filepath.Join(dataDir(), "updates", "TBS-Launcher.next.exe")
	if err := os.MkdirAll(filepath.Dir(next), 0o755); err != nil {
		logLine(err.Error())
		return
	}
	if err := stageNextExe(dest, next); err != nil {
		logLine("обновление не похоже на программу: " + err.Error())
		return
	}
	stateMu.Lock()
	state.UpdateReady = true
	stateMu.Unlock()
	logLine("обновление скачано. закрой лаунчер и открой снова")
}

func stageNextExe(src, next string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	magic := make([]byte, 2)
	if _, err := io.ReadFull(f, magic); err != nil {
		return err
	}
	if string(magic) == "PK" {
		r, err := zip.OpenReader(src)
		if err != nil {
			return err
		}
		defer r.Close()
		var pick *zip.File
		for _, zf := range r.File {
			if zf.FileInfo().IsDir() {
				continue
			}
			if strings.EqualFold(filepath.Base(zf.Name), "TBS-Launcher.exe") || strings.HasSuffix(strings.ToLower(zf.Name), ".exe") {
				if pick == nil || zf.UncompressedSize64 > pick.UncompressedSize64 {
					pick = zf
				}
			}
		}
		if pick == nil || pick.UncompressedSize64 < 1_000_000 {
			return fmt.Errorf("в архиве нет exe")
		}
		rc, err := pick.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		out, err := os.Create(next)
		if err != nil {
			return err
		}
		defer out.Close()
		if _, err := io.Copy(out, rc); err != nil {
			return err
		}
		return nil
	}
	if string(magic) != "MZ" {
		return fmt.Errorf("это не exe")
	}
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() < 1_000_000 {
		return fmt.Errorf("файл слишком маленький")
	}
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(next, in, 0o755)
}

func applyPendingUpdate() {
	next := filepath.Join(dataDir(), "updates", "TBS-Launcher.next.exe")
	st, err := os.Stat(next)
	if err != nil || st.Size() < 1_000_000 {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	exe, _ = filepath.Abs(exe)
	if !strings.EqualFold(filepath.Base(exe), "TBS-Launcher.exe") {
		return
	}
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return
	}
	data, err := os.ReadFile(next)
	if err != nil || len(data) < 2 || string(data[:2]) != "MZ" {
		_ = os.Rename(old, exe)
		return
	}
	if err := os.WriteFile(exe, data, 0o755); err != nil {
		_ = os.Rename(old, exe)
		return
	}
	_ = os.Remove(next)
	cmd := execCommand(exe)
	if err := cmd.Start(); err != nil {
		return
	}
	os.Exit(0)
}

func openMrpack(mrpackPath string) (*zip.ReadCloser, mrIndex, error) {
	r, err := zip.OpenReader(mrpackPath)
	if err != nil {
		return nil, mrIndex{}, err
	}
	var indexEntry *zip.File
	for _, f := range r.File {
		if f.Name == "modrinth.index.json" {
			indexEntry = f
			break
		}
	}
	if indexEntry == nil {
		r.Close()
		return nil, mrIndex{}, fmt.Errorf("в mrpack нет modrinth.index.json")
	}
	rc, err := indexEntry.Open()
	if err != nil {
		r.Close()
		return nil, mrIndex{}, err
	}
	body, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		r.Close()
		return nil, mrIndex{}, err
	}
	var index mrIndex
	if err := json.Unmarshal(body, &index); err != nil {
		r.Close()
		return nil, mrIndex{}, err
	}
	return r, index, nil
}

func listMods(mrpackPath string) ([]modInfo, error) {
	r, index, err := openMrpack(mrpackPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var mods []modInfo
	for _, file := range index.Files {
		client := file.Env["client"]
		if client == "" {
			client = "required"
		}
		if client == "unsupported" {
			continue
		}
		mods = append(mods, modInfo{
			Name: strings.TrimSuffix(strings.TrimSuffix(path.Base(file.Path), ".jar"), ".zip"),
			Kind: strings.Split(strings.ReplaceAll(file.Path, "\\", "/"), "/")[0],
			MB:   float64(int64(float64(file.FileSize)/104857.6)) / 10,
		})
	}
	for _, f := range r.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if !strings.HasPrefix(name, "overrides/mods/") || !strings.HasSuffix(strings.ToLower(name), ".jar") || f.FileInfo().IsDir() {
			continue
		}
		mods = append(mods, modInfo{
			Name: strings.TrimSuffix(path.Base(name), ".jar"),
			Kind: "mods",
			MB:   float64(int64(float64(f.UncompressedSize64)/104857.6)) / 10,
		})
	}
	if mods == nil {
		mods = []modInfo{}
	}
	return mods, nil
}

func installMrpack(mrpackPath string, hooks hook, previous *installedPack) (installedPack, error) {
	r, index, err := openMrpack(mrpackPath)
	if err != nil {
		return installedPack{}, err
	}
	defer r.Close()
	mc := index.Dependencies["minecraft"]
	nf := index.Dependencies["neoforge"]
	if mc == "" || nf == "" {
		return installedPack{}, fmt.Errorf("в сборке не указаны версии игры")
	}
	var files []mrFile
	for _, file := range index.Files {
		client := file.Env["client"]
		if client == "" {
			client = "required"
		}
		if client == "unsupported" {
			continue
		}
		files = append(files, file)
	}
	installed := map[string]string{}
	var mu sync.Mutex
	done := 0
	err = pool(3, len(files), func(i int) error {
		if hooks.cancelled() {
			return fmt.Errorf("отменено")
		}
		file := files[i]
		dest, err := safeJoin(gameDir(), file.Path)
		if err != nil {
			return err
		}
		sha := file.Hashes["sha1"]
		if sha != "" {
			if got, err := sha1File(dest); err == nil && strings.EqualFold(got, sha) {
				mu.Lock()
				installed[file.Path] = sha
				done++
				cur := done
				mu.Unlock()
				hooks.progress(progress{Phase: "моды", Current: int64(cur), Total: int64(len(files)), Label: filepath.Base(file.Path)})
				return nil
			}
		}
		if len(file.Downloads) == 0 || file.Downloads[0] == "" {
			return fmt.Errorf("нет ссылки для %s", file.Path)
		}
		logLine("качаю " + filepath.Base(file.Path))
		if err := downloadFile(file.Downloads[0], dest, sha, hooks, ""); err != nil {
			return err
		}
		got := sha
		if got == "" {
			got, err = sha1File(dest)
			if err != nil {
				return err
			}
		}
		mu.Lock()
		installed[file.Path] = got
		done++
		cur := done
		mu.Unlock()
		hooks.progress(progress{Phase: "моды", Current: int64(cur), Total: int64(len(files)), Label: filepath.Base(file.Path)})
		return nil
	})
	if err != nil {
		return installedPack{}, err
	}
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if !strings.HasPrefix(name, "overrides/") {
			continue
		}
		rel := strings.TrimPrefix(name, "overrides/")
		if rel == "" {
			continue
		}
		dest, err := safeJoin(gameDir(), rel)
		if err != nil {
			return installedPack{}, err
		}
		rc, err := f.Open()
		if err != nil {
			return installedPack{}, err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return installedPack{}, err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return installedPack{}, err
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return installedPack{}, err
		}
		installed["override:"+rel] = sha1Bytes(data)
	}
	if previous != nil {
		for rel, oldHash := range previous.Files {
			if _, ok := installed[rel]; ok {
				continue
			}
			diskRel := rel
			if strings.HasPrefix(rel, "override:") {
				diskRel = strings.TrimPrefix(rel, "override:")
			}
			dest, err := safeJoin(gameDir(), diskRel)
			if err != nil {
				continue
			}
			if got, err := sha1File(dest); err == nil && got == oldHash {
				_ = os.Remove(dest)
			}
		}
	}
	logLine("сборка " + index.VersionID + " на месте")
	return installedPack{
		PackVersion: index.VersionID,
		Minecraft:   mc,
		NeoForge:    nf,
		Name:        index.Name,
		Files:       installed,
	}, nil
}
