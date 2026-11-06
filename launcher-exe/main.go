package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed ui/index.html ui/app.css ui/app.js ui/icon.png manifest.json
var assets embed.FS

//go:embed pack/pack.mrpack
var bundledPack []byte

const launcherVersion = "1.1.2"

type progress struct {
	Phase   string `json:"phase"`
	Current int64  `json:"current"`
	Total   int64  `json:"total"`
	Label   string `json:"label"`
}

type hook struct {
	cancel func() bool
	report func(progress)
}

func (h hook) cancelled() bool {
	if h.cancel == nil {
		return false
	}
	return h.cancel()
}

func (h hook) progress(p progress) {
	if h.report != nil {
		h.report(p)
	}
}

type settings struct {
	RamGB      int    `json:"ramGb"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Fullscreen bool   `json:"fullscreen"`
	ClientID   string `json:"clientId"`
}

type loginView struct {
	UserCode        string `json:"userCode"`
	VerificationURI string `json:"verificationUri"`
	Message         string `json:"message"`
}

type appState struct {
	Product     string
	Busy        bool
	Phase       string
	Progress    *progress
	Error       string
	Log         []string
	Login       *loginView
	Manifest    manifest
	Mods        []modInfo
	UpdateReady bool
	Server      serverStatus
}

var (
	stateMu      sync.Mutex
	state        = appState{Product: "tbs-launcher", Phase: "ожидание", Mods: []modInfo{}}
	cancelFlag   bool
	pendingLogin *struct {
		clientID   string
		deviceCode string
		interval   int
		lastPoll   time.Time
	}
	boot = time.Now()
)

func logLine(text string) {
	line := time.Now().Format("15:04:05") + "  " + redact(text)
	stateMu.Lock()
	state.Log = append(state.Log, line)
	if len(state.Log) > 80 {
		state.Log = state.Log[len(state.Log)-80:]
	}
	stateMu.Unlock()
	_ = os.MkdirAll(dataDir(), 0o755)
	f, err := os.OpenFile(filepath.Join(dataDir(), "launcher.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		_, _ = f.WriteString(line + "\n")
		_ = f.Close()
	}
}

func settingsPath() string { return filepath.Join(dataDir(), "settings.json") }

func defaultSettings() settings {
	return settings{RamGB: 8, Width: 1920, Height: 1080}
}

var clientIDRe = regexp.MustCompile(`^[0-9a-fA-F-]{16,80}$`)

func sanitizeSettings(in settings) settings {
	ram := in.RamGB
	if ram == 0 {
		ram = 8
	}
	if ram < 2 {
		ram = 2
	}
	if ram > 24 {
		ram = 24
	}
	w, h := in.Width, in.Height
	if w == 0 {
		w = 1920
	}
	if h == 0 {
		h = 1080
	}
	if w < 640 {
		w = 640
	}
	if w > 7680 {
		w = 7680
	}
	if h < 480 {
		h = 480
	}
	if h > 4320 {
		h = 4320
	}
	id := ""
	if clientIDRe.MatchString(in.ClientID) {
		id = in.ClientID
	}
	return settings{RamGB: ram, Width: w, Height: h, Fullscreen: in.Fullscreen, ClientID: id}
}

func readSettings() settings {
	b, err := os.ReadFile(settingsPath())
	if err != nil {
		return defaultSettings()
	}
	var s settings
	if json.Unmarshal(b, &s) != nil {
		return defaultSettings()
	}
	return sanitizeSettings(s)
}

func writeSettings(s settings) settings {
	clean := sanitizeSettings(s)
	_ = os.MkdirAll(dataDir(), 0o755)
	b, _ := json.MarshalIndent(clean, "", "  ")
	_ = os.WriteFile(settingsPath(), b, 0o644)
	return clean
}

func setProgress(p progress) {
	stateMu.Lock()
	defer stateMu.Unlock()
	cp := p
	state.Progress = &cp
	phase := p.Phase
	if p.Total > 10000 && p.Total > 0 {
		phase = fmt.Sprintf("%s %d%%", p.Phase, p.Current*100/p.Total)
	} else if p.Total > 0 {
		phase = fmt.Sprintf("%s %d/%d", p.Phase, p.Current, p.Total)
	}
	if p.Label != "" {
		phase += " · " + p.Label
	}
	if phase != "" {
		state.Phase = phase
	}
}

func makeHooks() hook {
	return hook{
		cancel: func() bool {
			stateMu.Lock()
			defer stateMu.Unlock()
			return cancelFlag
		},
		report: setProgress,
	}
}

func publicJSON() []byte {
	stateMu.Lock()
	defer stateMu.Unlock()
	acc := readAccount()
	var accountView any
	if acc != nil {
		accountView = map[string]string{"name": acc.Profile.Name, "id": acc.Profile.ID}
	}
	logCopy := append([]string{}, state.Log...)
	if len(logCopy) > 12 {
		logCopy = logCopy[len(logCopy)-12:]
	}
	m := state.Manifest
	body := map[string]any{
		"product":  state.Product,
		"busy":     state.Busy,
		"phase":    state.Phase,
		"progress": state.Progress,
		"error":    state.Error,
		"log":      logCopy,
		"login":    state.Login,
		"account":  accountView,
		"settings": readSettings(),
		"manifest": map[string]any{
			"launcherVersion": launcherVersion,
			"packVersion":     m.PackVersion,
			"packName":        m.PackName,
			"minecraft":       m.Minecraft,
			"neoforge":        m.NeoForge,
			"server":          m.Server,
			"notes":           m.Notes,
		},
		"mods":         state.Mods,
		"updateReady":  state.UpdateReady,
		"installed":    readInstalled() != nil,
		"serverStatus": state.Server,
	}
	b, _ := json.Marshal(body)
	return b
}

func play() {
	stateMu.Lock()
	if state.Busy {
		stateMu.Unlock()
		return
	}
	state.Busy = true
	state.Error = ""
	cancelFlag = false
	state.Progress = nil
	stateMu.Unlock()
	defer func() {
		stateMu.Lock()
		state.Busy = false
		state.Progress = nil
		stateMu.Unlock()
	}()
	defer func() {
		if r := recover(); r != nil {
			fail(fmt.Errorf("сбой лаунчера: %v", r))
		}
	}()
	hooks := makeHooks()
	stateMu.Lock()
	m := state.Manifest
	stateMu.Unlock()
	if m.Minecraft == "" {
		m = resolveManifest()
		stateMu.Lock()
		state.Manifest = m
		stateMu.Unlock()
	}
	s := readSettings()
	acc, err := ensureFreshAccount()
	if err != nil {
		fail(err)
		return
	}
	logLine("лицензия: " + acc.Profile.Name)
	javaPath, err := ensureJava(hooks)
	if err != nil {
		fail(err)
		return
	}
	mcVer := m.Minecraft
	if mcVer == "" {
		mcVer = "1.21.1"
	}
	if err := ensureVanilla(mcVer, hooks); err != nil {
		fail(err)
		return
	}
	packPath, err := mrpackPathFor(m, hooks)
	if err != nil {
		fail(err)
		return
	}
	installed, err := installMrpack(packPath, hooks, readInstalled())
	if err != nil {
		fail(err)
		return
	}
	nf := installed.NeoForge
	if compareVer(nf, "21.1.49") < 0 {
		fail(fmt.Errorf("в сборке NeoForge %s, а нужно 21.1.49 или новее", nf))
		return
	}
	versionID, err := ensureNeoForge(javaPath, nf, hooks)
	if err != nil {
		fail(err)
		return
	}
	installed.PackVersion = m.PackVersion
	if installed.PackVersion == "" {
		installed.PackVersion = installed.Name
	}
	if err := writeInstalled(installed); err != nil {
		fail(err)
		return
	}
	stateMu.Lock()
	state.Phase = "запуск"
	stateMu.Unlock()
	logLine("старт игры, без прямого входа на сервер")
	if err := launchGame(launchOpts{
		GameDir: optsGame(), JavaPath: javaPath, VersionID: versionID,
		Profile: acc.Profile, AccessToken: acc.MCToken,
		RamMB: s.RamGB * 1024, Width: s.Width, Height: s.Height, Fullscreen: s.Fullscreen,
		Cancelled: hooks.cancelled,
	}, func(code int, detail string) {
		if code == 0 {
			logLine("игра закрыта")
			stateMu.Lock()
			if state.Phase == "в игре" {
				state.Phase = "ожидание"
			}
			stateMu.Unlock()
			return
		}
		if strings.TrimSpace(detail) == "" {
			detail = fmt.Sprintf("игра вылетела (код %d)", code)
		}
		fail(fmt.Errorf("%s", detail))
	}); err != nil {
		fail(err)
		return
	}
	logLine("игра запущена. лог — кнопка ЛОГ")
	stateMu.Lock()
	if state.Error == "" {
		state.Phase = "в игре"
	}
	stateMu.Unlock()
}

func optsGame() string { return gameDir() }

func collectLogs() map[string]string {
	gameText := readTail(filepath.Join(dataDir(), "game-output.log"), 140_000)
	latest := readTail(filepath.Join(gameDir(), "logs", "latest.log"), 140_000)
	if latest != "" {
		if gameText != "" {
			gameText += "\n\n----- latest.log -----\n"
		}
		gameText += latest
	}
	crash := newestCrash(filepath.Join(gameDir(), "crash-reports"))
	if crash == "" {
		crash = "краш-репорта нет"
	}
	launcher := readTail(filepath.Join(dataDir(), "launcher.log"), 80_000)
	if launcher == "" {
		launcher = "лог лаунчера пуст"
	}
	if strings.TrimSpace(gameText) == "" {
		gameText = "игра ещё не запускалась из этого лаунчера"
	}
	return map[string]string{"game": gameText, "launcher": launcher, "crash": crash}
}

func checkUpdates() {
	stateMu.Lock()
	if state.Busy {
		stateMu.Unlock()
		return
	}
	state.Busy = true
	state.Error = ""
	state.Phase = "проверка обновлений"
	stateMu.Unlock()
	defer func() {
		stateMu.Lock()
		state.Busy = false
		state.Progress = nil
		stateMu.Unlock()
	}()
	m := resolveManifest()
	stateMu.Lock()
	state.Manifest = m
	ready := state.UpdateReady
	stateMu.Unlock()
	if ready {
		logLine("обновление уже скачано, перезапускаю лаунчер")
		applyPendingUpdate()
		stateMu.Lock()
		state.Phase = "закрой окно крестиком и открой лаунчер снова"
		stateMu.Unlock()
		return
	}
	before := launcherVersion
	queueLauncherUpdate(m)
	stateMu.Lock()
	ready = state.UpdateReady
	stateMu.Unlock()
	if ready {
		logLine("обновление скачано. нажми ОБНОВИТЬ ещё раз, лаунчер перезапустится")
		stateMu.Lock()
		state.Phase = "обновление готово"
		stateMu.Unlock()
		return
	}
	if compareVer(before, m.LauncherVersion) >= 0 {
		logLine("лаунчер " + before + " последний. сборка " + m.PackVersion)
		stateMu.Lock()
		state.Phase = "обновлений нет"
		stateMu.Unlock()
		return
	}
	stateMu.Lock()
	state.Phase = "обновление не скачалось"
	stateMu.Unlock()
}

func fail(err error) {
	msg := redact(err.Error())
	stateMu.Lock()
	state.Error = msg
	state.Phase = "ошибка"
	stateMu.Unlock()
	logLine(msg)
}

func sendJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	switch b := body.(type) {
	case []byte:
		_, _ = w.Write(b)
	default:
		enc, _ := json.Marshal(body)
		_, _ = w.Write(enc)
	}
}

func readBody(r *http.Request) (map[string]any, error) {
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if len(bytesTrim(b)) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil, fmt.Errorf("битый json")
	}
	return m, nil
}

func bytesTrim(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

func num(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case string:
		i, _ := strconv.Atoi(n)
		return i
	default:
		return 0
	}
}

func handle(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" && !strings.HasPrefix(origin, "http://127.0.0.1:") {
		http.Error(w, "нет", http.StatusForbidden)
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			fail(fmt.Errorf("%v", rec))
			if w.Header().Get("Content-Type") == "" {
				sendJSON(w, 500, map[string]string{"error": "сбой"})
			}
		}
	}()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/state":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(publicJSON())
	case r.Method == http.MethodPost && r.URL.Path == "/api/settings":
		body, err := readBody(r)
		if err != nil {
			sendJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		cur := readSettings()
		if _, ok := body["ramGb"]; ok {
			cur.RamGB = num(body["ramGb"])
		}
		if _, ok := body["width"]; ok {
			cur.Width = num(body["width"])
		}
		if _, ok := body["height"]; ok {
			cur.Height = num(body["height"])
		}
		if v, ok := body["fullscreen"]; ok {
			b, _ := v.(bool)
			cur.Fullscreen = b
		}
		if v, ok := body["clientId"]; ok {
			cur.ClientID, _ = v.(string)
		}
		sendJSON(w, 200, map[string]any{"settings": writeSettings(cur)})
	case r.Method == http.MethodPost && r.URL.Path == "/api/login/start":
		s := readSettings()
		id := s.ClientID
		if id == "" {
			id = defaultClientID
		}
		dc, err := startDeviceCode(id)
		if err != nil {
			fail(err)
			sendJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		stateMu.Lock()
		pendingLogin = &struct {
			clientID   string
			deviceCode string
			interval   int
			lastPoll   time.Time
		}{clientID: id, deviceCode: dc.DeviceCode, interval: dc.Interval}
		state.Login = &loginView{UserCode: dc.UserCode, VerificationURI: dc.VerificationURI, Message: dc.Message}
		stateMu.Unlock()
		logLine("код входа: " + dc.UserCode)
		openExternal(dc.VerificationURI)
		sendJSON(w, 200, map[string]bool{"ok": true})
	case r.Method == http.MethodPost && r.URL.Path == "/api/login/poll":
		stateMu.Lock()
		p := pendingLogin
		stateMu.Unlock()
		if p == nil {
			sendJSON(w, 200, map[string]bool{"pending": false, "idle": true})
			return
		}
		if !p.lastPoll.IsZero() && time.Since(p.lastPoll) < time.Duration(p.interval)*time.Second {
			sendJSON(w, 200, map[string]bool{"pending": true})
			return
		}
		p.lastPoll = time.Now()
		done, token, err := pollDeviceCode(p.clientID, p.deviceCode)
		if err != nil {
			stateMu.Lock()
			pendingLogin = nil
			state.Login = nil
			stateMu.Unlock()
			fail(err)
			sendJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		if !done {
			sendJSON(w, 200, map[string]bool{"pending": true})
			return
		}
		acc, err := finishLogin(token, p.clientID)
		if err != nil {
			fail(err)
			sendJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		stateMu.Lock()
		pendingLogin = nil
		state.Login = nil
		stateMu.Unlock()
		logLine("лицензия привязана: " + acc.Profile.Name)
		sendJSON(w, 200, map[string]any{"pending": false, "name": acc.Profile.Name})
	case r.Method == http.MethodPost && r.URL.Path == "/api/logout":
		clearAccount()
		stateMu.Lock()
		pendingLogin = nil
		state.Login = nil
		stateMu.Unlock()
		sendJSON(w, 200, map[string]bool{"ok": true})
	case r.Method == http.MethodGet && r.URL.Path == "/api/logs":
		sendJSON(w, 200, collectLogs())
	case r.Method == http.MethodPost && r.URL.Path == "/api/update":
		go checkUpdates()
		sendJSON(w, 200, map[string]bool{"ok": true})
	case r.Method == http.MethodPost && r.URL.Path == "/api/play":
		go play()
		sendJSON(w, 200, map[string]bool{"ok": true})
	case r.Method == http.MethodPost && r.URL.Path == "/api/cancel":
		stateMu.Lock()
		cancelFlag = true
		stateMu.Unlock()
		logLine("отмена")
		sendJSON(w, 200, map[string]bool{"ok": true})
	case r.Method == http.MethodPost && r.URL.Path == "/api/repair":
		_ = os.Remove(filepath.Join(gameDir(), "tbs-vanilla.json"))
		_ = os.Remove(installedPath())
		versions := filepath.Join(gameDir(), "versions")
		entries, _ := os.ReadDir(versions)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "neoforge-") {
				_ = os.RemoveAll(filepath.Join(versions, e.Name()))
			} else {
				_ = os.RemoveAll(filepath.Join(versions, e.Name(), "natives"))
			}
		}
		logLine("файлы сброшены. следующий запуск заново проверит Java, NeoForge и моды")
		sendJSON(w, 200, map[string]bool{"ok": true})
	case (r.Method == http.MethodPost || r.Method == http.MethodGet) && r.URL.Path == "/api/quit":
		if time.Since(boot) < 3*time.Second {
			sendJSON(w, 200, map[string]bool{"ok": true, "ignored": true})
			return
		}
		sendJSON(w, 200, map[string]bool{"ok": true})
		go func() {
			time.Sleep(200 * time.Millisecond)
			os.Exit(0)
		}()
	case r.Method == http.MethodGet:
		name := "index.html"
		switch r.URL.Path {
		case "/", "/index.html":
			name = "index.html"
		case "/app.css":
			name = "app.css"
		case "/app.js":
			name = "app.js"
		case "/icon.png":
			name = "icon.png"
		default:
			http.NotFound(w, r)
			return
		}
		b, err := assets.ReadFile("ui/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ctype := "text/html; charset=utf-8"
		if strings.HasSuffix(name, ".css") {
			ctype = "text/css; charset=utf-8"
		}
		if strings.HasSuffix(name, ".js") {
			ctype = "text/javascript; charset=utf-8"
		}
		if strings.HasSuffix(name, ".png") {
			ctype = "image/png"
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	default:
		http.NotFound(w, r)
	}
}

func listenPort() int {
	if v := os.Getenv("TBS_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 47821
}

func alreadyRunning(port int) bool {
	client := &http.Client{Timeout: 700 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/state", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var data struct {
		Product string `json:"product"`
	}
	if json.NewDecoder(resp.Body).Decode(&data) != nil {
		return false
	}
	return data.Product == "tbs-launcher"
}

func main() {
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprint(r)
			_ = os.MkdirAll(dataDir(), 0o755)
			_ = os.WriteFile(filepath.Join(dataDir(), "crash.txt"), []byte(msg), 0o644)
			alert("Лаунчер упал.\n" + msg + "\n\nЛог: " + filepath.Join(dataDir(), "launcher.log"))
		}
	}()
	applyPendingUpdate()
	port := listenPort()
	if alreadyRunning(port) {
		_ = openWindow(fmt.Sprintf("http://127.0.0.1:%d/", port))
		return
	}
	_ = os.MkdirAll(cacheDir(), 0o755)
	_ = os.MkdirAll(gameDir(), 0o755)
	m, err := readBundledManifest()
	if err != nil {
		alert("В лаунчере нет манифеста сборки: " + err.Error())
		return
	}
	state.Manifest = m
	packPath, err := bundledMrpackPath()
	if err == nil {
		if mods, err := listMods(packPath); err == nil {
			state.Mods = mods
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", handle)
	srv := &http.Server{Handler: mux}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		alert("Порт лаунчера занят. Закрой второй TBS Launcher и открой снова.\n" + err.Error())
		return
	}
	go func() {
		resolved := resolveManifest()
		stateMu.Lock()
		state.Manifest = resolved
		host, p := resolved.Server.Host, resolved.Server.Port
		stateMu.Unlock()
		go queueLauncherUpdate(resolved)
		if host != "" && p != 0 {
			st := pingServer(host, p)
			stateMu.Lock()
			state.Server = st
			stateMu.Unlock()
		}
		tick := time.NewTicker(20 * time.Second)
		defer tick.Stop()
		for range tick.C {
			if host == "" || p == 0 {
				continue
			}
			st := pingServer(host, p)
			stateMu.Lock()
			state.Server = st
			stateMu.Unlock()
		}
	}()
	go func() { _ = srv.Serve(ln) }()
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	logLine("лаунчер слушает " + url)
	if os.Getenv("TBS_HEADLESS") == "1" {
		select {}
	}
	if err := openWindow(url); err != nil {
		alert("Не нашёл Microsoft Edge. Он встроен в Windows 10 и 11.\nПоставь Edge и открой лаунчер снова.\nЕсли Edge есть, а окно не вылезло — напиши мне лог:\n" + filepath.Join(dataDir(), "launcher.log"))
		return
	}
	select {}
}
