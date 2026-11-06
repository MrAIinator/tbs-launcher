package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

const versionManifestURL = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"

type artifact struct {
	Path   string `json:"path"`
	URL    string `json:"url"`
	SHA1   string `json:"sha1"`
	Size   int64  `json:"size"`
	Native bool   `json:"-"`
}

type rule struct {
	Action   string          `json:"action"`
	OS       *struct {
		Name string `json:"name"`
		Arch string `json:"arch"`
	} `json:"os"`
	Features map[string]bool `json:"features"`
}

func osName() string {
	switch runtime.GOOS {
	case "windows":
		return "windows"
	case "darwin":
		return "osx"
	default:
		return "linux"
	}
}

func archOK(ruleArch string) bool {
	if ruleArch == "" {
		return true
	}
	switch runtime.GOARCH {
	case "amd64":
		return ruleArch == "x64" || ruleArch == "amd64" || ruleArch == "x86_64"
	case "386":
		return ruleArch == "x86" || ruleArch == "ia32" || ruleArch == "386"
	case "arm64":
		return ruleArch == "arm64" || ruleArch == "aarch64"
	default:
		return ruleArch == runtime.GOARCH
	}
}

func rulesAllow(rules []rule, features map[string]bool) bool {
	if len(rules) == 0 {
		return true
	}
	matched := false
	allow := false
	for _, rule := range rules {
		if rule.OS != nil {
			if rule.OS.Name != "" && rule.OS.Name != osName() {
				continue
			}
			if !archOK(rule.OS.Arch) {
				continue
			}
		}
		if len(rule.Features) > 0 {
			ok := true
			for k, v := range rule.Features {
				if features[k] != v {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
		}
		matched = true
		allow = rule.Action == "allow"
	}
	if !matched {
		return false
	}
	return allow
}

func fetchJSON(rawURL string, dest any) error {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "TBSLauncher/"+launcherVersion)
	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d %s", resp.StatusCode, rawURL)
	}
	return json.NewDecoder(resp.Body).Decode(dest)
}

func ensureVanilla(versionID string, hooks hook) error {
	markerPath := filepath.Join(gameDir(), "tbs-vanilla.json")
	jar := filepath.Join(gameDir(), "versions", versionID, versionID+".jar")
	if b, err := os.ReadFile(markerPath); err == nil {
		var marker struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(b, &marker) == nil && marker.Version == versionID {
			if st, err := os.Stat(jar); err == nil && st.Size() > 0 {
				logLine("Minecraft " + versionID + " уже на месте")
				return nil
			}
		}
	}
	logLine("качаю Minecraft " + versionID)
	var manifest struct {
		Versions []struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"versions"`
	}
	if err := fetchJSON(versionManifestURL, &manifest); err != nil {
		return err
	}
	var metaURL string
	for _, v := range manifest.Versions {
		if v.ID == versionID {
			metaURL = v.URL
			break
		}
	}
	if metaURL == "" {
		return fmt.Errorf("в манифесте Mojang нет %s", versionID)
	}
	var version struct {
		ID        string `json:"id"`
		Downloads struct {
			Client *artifact `json:"client"`
		} `json:"downloads"`
		Libraries []library `json:"libraries"`
		AssetIndex *struct {
			ID   string `json:"id"`
			URL  string `json:"url"`
			SHA1 string `json:"sha1"`
		} `json:"assetIndex"`
	}
	if err := fetchJSON(metaURL, &version); err != nil {
		return err
	}
	versionDir := filepath.Join(gameDir(), "versions", versionID)
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(version)
	if err != nil {
		return err
	}
	// keep the full version json, not the partial struct
	req, err := http.NewRequest(http.MethodGet, metaURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "TBSLauncher/"+launcherVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	full, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d version json", resp.StatusCode)
	}
	if err := os.WriteFile(filepath.Join(versionDir, versionID+".json"), full, 0o644); err != nil {
		return err
	}
	_ = raw
	clientArt := version.Downloads.Client
	if clientArt == nil || clientArt.URL == "" {
		return fmt.Errorf("у версии нет client.jar")
	}
	if err := downloadFile(clientArt.URL, jar, clientArt.SHA1, hooks, "игра"); err != nil {
		return err
	}
	var libs []artifact
	for _, lib := range version.Libraries {
		if !rulesAllow(lib.Rules, nil) || !nativeClassifierOK(lib.Name) {
			continue
		}
		if lib.Downloads.Artifact != nil && lib.Downloads.Artifact.URL != "" && lib.Downloads.Artifact.Path != "" {
			libs = append(libs, *lib.Downloads.Artifact)
		}
		if native := nativeArtifact(lib); native != nil && native.URL != "" && native.Path != "" {
			libs = append(libs, *native)
		}
	}
	var done atomic.Int64
	if err := pool(8, len(libs), func(i int) error {
		if hooks.cancelled() {
			return fmt.Errorf("отменено")
		}
		dest, err := safeJoin(filepath.Join(gameDir(), "libraries"), libs[i].Path)
		if err != nil {
			return err
		}
		if err := downloadFile(libs[i].URL, dest, libs[i].SHA1, hooks, ""); err != nil {
			return err
		}
		n := done.Add(1)
		if n%20 == 0 || int(n) == len(libs) {
			hooks.progress(progress{Phase: "библиотеки", Current: n, Total: int64(len(libs)), Label: "libraries"})
		}
		return nil
	}); err != nil {
		return err
	}
	if version.AssetIndex == nil || version.AssetIndex.URL == "" || version.AssetIndex.ID == "" {
		return fmt.Errorf("нет индекса ассетов")
	}
	indexPath := filepath.Join(gameDir(), "assets", "indexes", version.AssetIndex.ID+".json")
	if err := downloadFile(version.AssetIndex.URL, indexPath, version.AssetIndex.SHA1, hooks, ""); err != nil {
		return err
	}
	indexBody, err := os.ReadFile(indexPath)
	if err != nil {
		return err
	}
	var index struct {
		Objects map[string]struct {
			Hash string `json:"hash"`
			Size int64  `json:"size"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(indexBody, &index); err != nil {
		return err
	}
	type obj struct {
		hash string
		size int64
	}
	objects := make([]obj, 0, len(index.Objects))
	for _, o := range index.Objects {
		objects = append(objects, obj{hash: o.Hash, size: o.Size})
	}
	var assetsDone atomic.Int64
	if err := pool(12, len(objects), func(i int) error {
		if hooks.cancelled() {
			return fmt.Errorf("отменено")
		}
		o := objects[i]
		if len(o.hash) < 2 {
			return fmt.Errorf("битый хэш ассета")
		}
		rel := o.hash[:2] + "/" + o.hash
		dest, err := safeJoin(filepath.Join(gameDir(), "assets", "objects"), rel)
		if err != nil {
			return err
		}
		if st, err := os.Stat(dest); err == nil && st.Size() == o.size {
			n := assetsDone.Add(1)
			if n%200 == 0 || int(n) == len(objects) {
				hooks.progress(progress{Phase: "ассеты", Current: n, Total: int64(len(objects)), Label: "assets"})
			}
			return nil
		}
		u := "https://resources.download.minecraft.net/" + rel
		if err := downloadFile(u, dest, o.hash, hooks, ""); err != nil {
			return err
		}
		n := assetsDone.Add(1)
		if n%200 == 0 || int(n) == len(objects) {
			hooks.progress(progress{Phase: "ассеты", Current: n, Total: int64(len(objects)), Label: "assets"})
		}
		return nil
	}); err != nil {
		return err
	}
	if clientArt.SHA1 != "" {
		got, err := sha1File(jar)
		if err != nil {
			return err
		}
		if !strings.EqualFold(got, clientArt.SHA1) {
			return fmt.Errorf("client.jar не сошёлся")
		}
	}
	b, _ := json.Marshal(map[string]any{"version": versionID, "assets": len(objects)})
	if err := os.WriteFile(markerPath, b, 0o644); err != nil {
		return err
	}
	logLine("Minecraft " + versionID + " готов")
	return nil
}

func findJava(dir string, depth int) string {
	entries, err := os.ReadDir(dir)
	if err != nil || depth > 6 {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if e.Name() == "java.exe" || e.Name() == "java" {
			return filepath.Join(dir, e.Name())
		}
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if found := findJava(filepath.Join(dir, e.Name()), depth+1); found != "" {
			return found
		}
	}
	return ""
}

func ensureJava(hooks hook) (string, error) {
	if existing := usableJava(filepath.Join(runtimeDir(), "jdk")); existing != "" {
		return existing, nil
	}
	if existing := usableJava(filepath.Join(runtimeDir(), "java")); existing != "" {
		return existing, nil
	}
	if runtime.GOOS != "windows" {
		return "", fmt.Errorf("Java 21 лаунчер качает под Windows")
	}
	logLine("качаю Java 21, полный JDK. Прошлый JRE NeoForge убивает на старте")
	zipPath := filepath.Join(cacheDir(), "temurin-21-jdk.zip")
	url := "https://api.adoptium.net/v3/binary/latest/21/ga/windows/x64/jdk/hotspot/normal/eclipse?project=jdk"
	if st, err := os.Stat(zipPath); err == nil && st.Size() < 20_000_000 {
		_ = os.Remove(zipPath)
	}
	if err := downloadFile(url, zipPath, "", hooks, "java"); err != nil {
		return "", err
	}
	dest := filepath.Join(runtimeDir(), "jdk")
	_ = os.RemoveAll(dest)
	if err := unzipAll(zipPath, dest); err != nil {
		_ = os.Remove(zipPath)
		return "", err
	}
	java := usableJava(dest)
	if java == "" {
		return "", fmt.Errorf("в архиве Java нет JDK с модулем jdk.naming.dns")
	}
	_ = os.RemoveAll(filepath.Join(runtimeDir(), "java"))
	logLine("Java 21 на месте")
	return java, nil
}

func usableJava(dir string) string {
	bin := findJava(dir, 0)
	if bin == "" || !javaHasNamingDNS(bin) {
		return ""
	}
	return bin
}

func javaHasNamingDNS(bin string) bool {
	home := filepath.Dir(filepath.Dir(bin))
	if _, err := os.Stat(filepath.Join(home, "jmods", "jdk.naming.dns.jmod")); err == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--list-modules")
	hideConsole(cmd)
	out, err := cmd.Output()
	return err == nil && bytes.Contains(out, []byte("jdk.naming.dns@"))
}

func unzipAll(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for _, f := range r.File {
		target, err := safeJoin(dest, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func ensureLauncherProfile(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	profile := filepath.Join(dir, "launcher_profiles.json")
	if _, err := os.Stat(profile); err == nil {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	body := map[string]any{
		"profiles": map[string]any{
			"tbs": map[string]any{
				"created":       now,
				"icon":          "Grass",
				"lastUsed":      now,
				"lastVersionId": "1.21.1",
				"name":          "TBS",
				"type":          "custom",
			},
		},
		"selectedProfile": "tbs",
		"clientToken":     "tbs-launcher",
	}
	b, _ := json.MarshalIndent(body, "", "  ")
	return os.WriteFile(profile, b, 0o644)
}

func ensureNeoForge(javaPath, version string, hooks hook) (string, error) {
	id := "neoforge-" + version
	jsonPath := filepath.Join(gameDir(), "versions", id, id+".json")
	if st, err := os.Stat(jsonPath); err == nil && st.Size() > 0 {
		logLine("NeoForge " + version + " уже стоит")
		return id, nil
	}
	jar := filepath.Join(cacheDir(), "neoforge-"+version+"-installer.jar")
	u := "https://maven.neoforged.net/releases/net/neoforged/neoforge/" + version + "/neoforge-" + version + "-installer.jar"
	logLine("качаю NeoForge " + version)
	if err := downloadFile(u, jar, "", hooks, "neoforge"); err != nil {
		return "", err
	}
	if err := ensureLauncherProfile(gameDir()); err != nil {
		return "", err
	}
	logLine("ставлю NeoForge, это не быстро")
	if err := os.MkdirAll(gameDir(), 0o755); err != nil {
		return "", err
	}
	cmd := execCommand(javaPath, "-Djava.awt.headless=true", "-jar", jar, "--installClient", gameDir())
	cmd.Dir = cacheDir()
	hideConsole(cmd)
	out, err := cmd.CombinedOutput()
	tail := string(out)
	if len(tail) > 800 {
		tail = tail[len(tail)-800:]
	}
	lines := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
	last := ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			last = line
		}
	}
	if last != "" {
		if len(last) > 180 {
			last = last[:180]
		}
		logLine(last)
	}
	if err != nil {
		return "", fmt.Errorf("установщик NeoForge вылетел: %s\n%s", err.Error(), tail)
	}
	if st, err := os.Stat(jsonPath); err != nil || st.Size() == 0 {
		versions := filepath.Join(gameDir(), "versions")
		entries, _ := os.ReadDir(versions)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "neoforge-") {
				return e.Name(), nil
			}
		}
		return "", fmt.Errorf("установщик не создал версию NeoForge")
	}
	return id, nil
}
