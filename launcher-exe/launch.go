package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type library struct {
	Name      string            `json:"name"`
	Rules     []rule            `json:"rules"`
	Natives   map[string]string `json:"natives"`
	Downloads struct {
		Artifact    *artifact            `json:"artifact"`
		Classifiers map[string]*artifact `json:"classifiers"`
	} `json:"downloads"`
}

type versionFile struct {
	ID                 string `json:"id"`
	InheritsFrom       string `json:"inheritsFrom"`
	MainClass          string `json:"mainClass"`
	MinecraftArguments string `json:"minecraftArguments"`
	JarID              string `json:"-"`
	AssetIndex         *struct {
		ID string `json:"id"`
	} `json:"assetIndex"`
	Downloads struct {
		Client *artifact `json:"client"`
	} `json:"downloads"`
	Libraries []library `json:"libraries"`
	Arguments struct {
		Game []json.RawMessage `json:"game"`
		JVM  []json.RawMessage `json:"jvm"`
	} `json:"arguments"`
}

func nativeClassifier() string {
	if runtime.GOARCH == "386" {
		return "32"
	}
	return "64"
}

func nativeArtifact(lib library) *artifact {
	if len(lib.Natives) == 0 {
		return nil
	}
	key := lib.Natives[osName()]
	if key == "" {
		return nil
	}
	classifier := strings.ReplaceAll(key, "${arch}", nativeClassifier())
	art := lib.Downloads.Classifiers[classifier]
	if art == nil {
		return nil
	}
	cp := *art
	cp.Native = true
	return &cp
}

func artifactsOf(lib library) []artifact {
	var out []artifact
	if lib.Downloads.Artifact != nil && lib.Downloads.Artifact.Path != "" {
		out = append(out, *lib.Downloads.Artifact)
	}
	if n := nativeArtifact(lib); n != nil && n.Path != "" {
		out = append(out, *n)
	}
	return out
}

func readVersion(dir, id string) (versionFile, error) {
	b, err := os.ReadFile(filepath.Join(dir, "versions", id, id+".json"))
	if err != nil {
		return versionFile{}, err
	}
	var v versionFile
	if err := json.Unmarshal(b, &v); err != nil {
		return versionFile{}, err
	}
	if v.ID == "" {
		v.ID = id
	}
	return v, nil
}

func mergeVersion(dir, id string, seen map[string]bool) (versionFile, error) {
	if seen[id] {
		return versionFile{}, fmt.Errorf("цикл версий %s", id)
	}
	seen[id] = true
	cur, err := readVersion(dir, id)
	if err != nil {
		return versionFile{}, err
	}
	if cur.InheritsFrom == "" {
		cur.JarID = id
		return cur, nil
	}
	parent, err := mergeVersion(dir, cur.InheritsFrom, seen)
	if err != nil {
		return versionFile{}, err
	}
	merged := parent
	merged.ID = cur.ID
	merged.JarID = parent.JarID
	if cur.MainClass != "" {
		merged.MainClass = cur.MainClass
	}
	if cur.AssetIndex != nil {
		merged.AssetIndex = cur.AssetIndex
	}
	if cur.Downloads.Client != nil {
		merged.Downloads = cur.Downloads
	}
	merged.Libraries = append(append([]library{}, parent.Libraries...), cur.Libraries...)
	merged.Arguments.Game = append(append([]json.RawMessage{}, parent.Arguments.Game...), cur.Arguments.Game...)
	merged.Arguments.JVM = append(append([]json.RawMessage{}, parent.Arguments.JVM...), cur.Arguments.JVM...)
	if cur.MinecraftArguments != "" {
		merged.MinecraftArguments = cur.MinecraftArguments
	}
	merged.InheritsFrom = ""
	return merged, nil
}

func collectArgs(raws []json.RawMessage, features map[string]bool) []string {
	var out []string
	for _, raw := range raws {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			continue
		}
		if raw[0] == '"' {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				out = append(out, s)
			}
			continue
		}
		var obj struct {
			Rules []rule          `json:"rules"`
			Value json.RawMessage `json:"value"`
		}
		if json.Unmarshal(raw, &obj) != nil {
			continue
		}
		if !rulesAllow(obj.Rules, features) {
			continue
		}
		v := bytes.TrimSpace(obj.Value)
		if len(v) == 0 {
			continue
		}
		if v[0] == '"' {
			var s string
			if json.Unmarshal(v, &s) == nil {
				out = append(out, s)
			}
		} else {
			var arr []string
			if json.Unmarshal(v, &arr) == nil {
				out = append(out, arr...)
			}
		}
	}
	return out
}

func subst(value string, vars map[string]string) string {
	var b strings.Builder
	for {
		i := strings.Index(value, "${")
		if i < 0 {
			b.WriteString(value)
			break
		}
		b.WriteString(value[:i])
		j := strings.Index(value[i+2:], "}")
		if j < 0 {
			b.WriteString(value[i:])
			break
		}
		key := value[i+2 : i+2+j]
		b.WriteString(vars[key])
		value = value[i+2+j+1:]
	}
	return b.String()
}

func extractNatives(jarPath, dest string) error {
	r, err := zip.OpenReader(jarPath)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		low := strings.ToLower(f.Name)
		if !strings.HasSuffix(low, ".dll") && !strings.HasSuffix(low, ".so") && !strings.HasSuffix(low, ".dylib") {
			continue
		}
		target, err := safeJoin(dest, filepath.Base(f.Name))
		if err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(target)
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

type launchOpts struct {
	GameDir     string
	JavaPath    string
	VersionID   string
	Profile     profile
	AccessToken string
	RamMB       int
	Width       int
	Height      int
	Fullscreen  bool
	Host        string
	Port        int
	Cancelled   func() bool
}

func forArgFile(arg string) string {
	if runtime.GOOS == "windows" {
		arg = strings.ReplaceAll(arg, "\\", "/")
	}
	if strings.ContainsAny(arg, " \t\"") {
		return `"` + strings.ReplaceAll(arg, `"`, `\"`) + `"`
	}
	return arg
}

func buildPlan(opts launchOpts) ([]string, error) {
	version, err := mergeVersion(opts.GameDir, opts.VersionID, map[string]bool{})
	if err != nil {
		return nil, err
	}
	order := []string{}
	byPath := map[string]artifact{}
	for _, lib := range version.Libraries {
		if !rulesAllow(lib.Rules, nil) || !nativeClassifierOK(lib.Name) {
			continue
		}
		for _, art := range artifactsOf(lib) {
			if _, ok := byPath[art.Path]; !ok {
				order = append(order, art.Path)
			}
			byPath[art.Path] = art
		}
	}
	for _, p := range order {
		art := byPath[p]
		dest, err := safeJoin(filepath.Join(opts.GameDir, "libraries"), art.Path)
		if err != nil {
			return nil, err
		}
		if art.URL != "" {
			if err := downloadFile(art.URL, dest, art.SHA1, hook{cancel: opts.Cancelled}, ""); err != nil {
				return nil, err
			}
		} else if _, err := os.Stat(dest); err != nil {
			return nil, fmt.Errorf("нет библиотеки %s", art.Path)
		}
	}
	nativesDir := filepath.Join(opts.GameDir, "versions", opts.VersionID, "natives")
	_ = os.RemoveAll(nativesDir)
	if err := os.MkdirAll(nativesDir, 0o755); err != nil {
		return nil, err
	}
	var cp []string
	for _, p := range order {
		art := byPath[p]
		full := filepath.Join(opts.GameDir, "libraries", art.Path)
		if art.Native || jarIsNatives(art.Path) {
			if err := extractNatives(full, nativesDir); err != nil {
				return nil, err
			}
			continue
		}
		cp = append(cp, full)
	}
	clientJar := filepath.Join(opts.GameDir, "versions", version.JarID, version.JarID+".jar")
	if st, err := os.Stat(clientJar); err != nil || st.Size() == 0 {
		return nil, fmt.Errorf("нет %s.jar", version.JarID)
	}
	cp = append(cp, clientJar)
	sep := string(os.PathListSeparator)
	assetID := "17"
	if version.AssetIndex != nil && version.AssetIndex.ID != "" {
		assetID = version.AssetIndex.ID
	}
	xms := opts.RamMB
	if xms > 1024 {
		xms = 1024
	}
	vars := map[string]string{
		"natives_directory":     nativesDir,
		"launcher_name":         "TBS",
		"launcher_version":      launcherVersion,
		"classpath":             strings.Join(cp, sep),
		"classpath_separator":   sep,
		"library_directory":     filepath.Join(opts.GameDir, "libraries"),
		"auth_player_name":      opts.Profile.Name,
		"version_name":          version.ID,
		"game_directory":        opts.GameDir,
		"assets_root":           filepath.Join(opts.GameDir, "assets"),
		"assets_index_name":     assetID,
		"auth_uuid":             dashedUUID(opts.Profile.ID),
		"auth_access_token":     opts.AccessToken,
		"user_type":             "msa",
		"version_type":          "TBS",
		"resolution_width":      fmt.Sprint(opts.Width),
		"resolution_height":     fmt.Sprint(opts.Height),
		"quickPlayPath":         "",
		"quickPlaySingleplayer": "",
		"quickPlayMultiplayer":  "",
		"quickPlayRealms":       "",
		"clientid":              "tbs-launcher",
		"auth_xuid":             opts.Profile.XUID,
	}
	if vars["auth_xuid"] == "" {
		vars["auth_xuid"] = "0"
	}
	features := map[string]bool{
		"has_custom_resolution": !opts.Fullscreen,
	}
	jvm := collectArgs(version.Arguments.JVM, features)
	game := collectArgs(version.Arguments.Game, features)
	for i, a := range jvm {
		jvm[i] = subst(a, vars)
	}
	for i, a := range game {
		game[i] = subst(a, vars)
	}
	if version.MinecraftArguments != "" && len(version.Arguments.Game) == 0 {
		for _, a := range strings.Fields(subst(version.MinecraftArguments, vars)) {
			game = append(game, a)
		}
	}
	if opts.Fullscreen && !contains(game, "--fullscreen") {
		game = append(game, "--fullscreen")
	}
	jvm = stripQuickPlay(jvm)
	game = stripQuickPlay(game)
	if !hasArgPrefix(jvm, "-Dorg.lwjgl.librarypath=") {
		jvm = append(jvm, "-Dorg.lwjgl.librarypath="+nativesDir)
	}
	args := []string{fmt.Sprintf("-Xms%dM", xms), fmt.Sprintf("-Xmx%dM", opts.RamMB)}
	args = append(args, jvm...)
	args = append(args, version.MainClass)
	args = append(args, game...)
	args = stripClasspath(args, sep)
	return args, nil
}

func jarIsNatives(path string) bool {
	return strings.Contains(strings.ToLower(filepath.Base(path)), "natives-")
}

func hasArgPrefix(args []string, prefix string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return true
		}
	}
	return false
}

func stripQuickPlay(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--quickPlayMultiplayer", "--quickPlaySingleplayer", "--quickPlayRealms", "--quickPlayPath":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
			}
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func normalizePath(p string) string {
	return strings.ToLower(strings.ReplaceAll(p, "\\", "/"))
}

func stripClasspath(args []string, sep string) []string {
	modSet := map[string]bool{}
	var ignores []string
	for i := 0; i < len(args); i++ {
		if (args[i] == "-p" || args[i] == "--module-path") && i+1 < len(args) {
			for _, p := range strings.Split(args[i+1], sep) {
				if p != "" {
					modSet[normalizePath(p)] = true
				}
			}
		}
		if strings.HasPrefix(args[i], "-DignoreList=") {
			for _, ig := range strings.Split(strings.TrimPrefix(args[i], "-DignoreList="), ",") {
				ig = strings.TrimSpace(ig)
				if ig != "" {
					ignores = append(ignores, ig)
				}
			}
		}
	}
	for i := 0; i < len(args)-1; i++ {
		if args[i] != "-cp" && args[i] != "-classpath" {
			continue
		}
		var kept []string
		for _, p := range strings.Split(args[i+1], sep) {
			if p == "" {
				continue
			}
			if modSet[normalizePath(p)] || jarIsNatives(p) {
				continue
			}
			base := filepath.Base(p)
			skip := false
			for _, ig := range ignores {
				if strings.HasPrefix(base, ig) {
					skip = true
					break
				}
			}
			if skip {
				continue
			}
			kept = append(kept, p)
		}
		args[i+1] = strings.Join(kept, sep)
	}
	return args
}

func contains(list []string, needle string) bool {
	for _, s := range list {
		if s == needle {
			return true
		}
	}
	return false
}

func dashedUUID(id string) string {
	raw := strings.ReplaceAll(strings.TrimSpace(id), "-", "")
	if len(raw) != 32 {
		return id
	}
	return raw[0:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:32]
}

func nativeClassifierOK(name string) bool {
	parts := strings.Split(name, ":")
	if len(parts) < 4 {
		return true
	}
	classifier := parts[3]
	if !strings.HasPrefix(classifier, "natives-") {
		return true
	}
	switch osName() {
	case "windows":
		switch runtime.GOARCH {
		case "amd64":
			return classifier == "natives-windows"
		case "386":
			return classifier == "natives-windows-x86"
		case "arm64":
			return classifier == "natives-windows-arm64"
		}
	case "linux":
		switch runtime.GOARCH {
		case "amd64":
			return classifier == "natives-linux"
		case "arm64":
			return classifier == "natives-linux-arm64" || classifier == "natives-linux-aarch64"
		}
	case "osx":
		if classifier == "natives-macos-patch" {
			return true
		}
		if runtime.GOARCH == "arm64" {
			return classifier == "natives-macos-arm64"
		}
		return classifier == "natives-macos" || classifier == "natives-macos-x86_64"
	}
	return false
}

func javaWindowBinary(javaPath string) string {
	if runtime.GOOS != "windows" {
		return javaPath
	}
	javaw := filepath.Join(filepath.Dir(javaPath), "javaw.exe")
	if st, err := os.Stat(javaw); err == nil && !st.IsDir() {
		return javaw
	}
	return javaPath
}

func launchGame(opts launchOpts, onExit func(code int, detail string)) error {
	if opts.Cancelled == nil {
		opts.Cancelled = func() bool { return false }
	}
	args, err := buildPlan(opts)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir(), 0o755); err != nil {
		return err
	}
	argFile := filepath.Join(dataDir(), "launch.args")
	var b strings.Builder
	for _, arg := range args {
		b.WriteString(forArgFile(arg))
		b.WriteByte('\n')
	}
	if err := os.WriteFile(argFile, []byte(b.String()), 0o644); err != nil {
		return err
	}
	logPath := filepath.Join(dataDir(), "game-output.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	bin := javaWindowBinary(opts.JavaPath)
	cmd := execCommand(bin, "@"+argFile)
	cmd.Dir = opts.GameDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	detach(cmd)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	go func() {
		waitErr := cmd.Wait()
		_ = logFile.Close()
		code := 0
		if waitErr != nil {
			if ee, ok := waitErr.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				code = -1
			}
		}
		if onExit != nil {
			onExit(code, crashDetail(opts.GameDir, logPath, code))
		}
	}()
	return nil
}

func crashDetail(gameDir, outputLog string, code int) string {
	if code == 0 {
		return ""
	}
	var blobs []string
	if text := readTail(filepath.Join(gameDir, "logs", "latest.log"), 80_000); text != "" {
		blobs = append(blobs, text)
	}
	if text := readTail(outputLog, 80_000); text != "" {
		blobs = append(blobs, text)
	}
	if text := newestCrash(filepath.Join(gameDir, "crash-reports")); text != "" {
		blobs = append(blobs, text)
	}
	lines := exceptionLines(strings.Join(blobs, "\n"))
	if len(lines) == 0 {
		return fmt.Sprintf("игра вылетела (код %d). открой кнопку ЛОГ", code)
	}
	msg := strings.Join(lines, "\n")
	if len(msg) > 700 {
		msg = msg[:700]
	}
	return fmt.Sprintf("игра вылетела (код %d)\n%s", code, strings.TrimSpace(msg))
}

func readTail(path string, max int) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return ""
	}
	if len(b) > max {
		b = b[len(b)-max:]
	}
	return string(b)
}

func newestCrash(dir string) string {
	path := newestCrashPath(dir)
	if path == "" {
		return ""
	}
	return readTail(path, 80_000)
}

func newestCrashPath(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return ""
	}
	var best string
	var bestTime time.Time
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestTime) {
			best = filepath.Join(dir, e.Name())
			bestTime = info.ModTime()
		}
	}
	return best
}

func exceptionLines(text string) []string {
	raw := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out []string
	seen := map[string]bool{}
	for _, line := range raw {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "at ") {
			continue
		}
		low := strings.ToLower(trim)
		ok := strings.Contains(trim, "Exception") ||
			strings.Contains(trim, "Error:") ||
			strings.HasPrefix(trim, "Caused by:") ||
			strings.Contains(trim, "Description:") ||
			strings.Contains(trim, "Module named") ||
			(strings.Contains(low, "mixin") && strings.Contains(low, "fail"))
		if !ok || seen[trim] {
			continue
		}
		seen[trim] = true
		out = append(out, trim)
	}
	if len(out) > 8 {
		out = out[len(out)-8:]
	}
	return out
}
