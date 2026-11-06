package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCompareVer(t *testing.T) {
	if compareVer("1.0.0", "1.1.0") >= 0 {
		t.Fatal("1.0.0 should be older")
	}
	if compareVer("21.1.249", "21.1.49") <= 0 {
		t.Fatal("249 is newer than 49")
	}
	if compareVer("1.1.0", "1.1.0") != 0 {
		t.Fatal("equal")
	}
}

func TestSafeJoin(t *testing.T) {
	root := t.TempDir()
	if _, err := safeJoin(root, "../x"); err == nil {
		t.Fatal("dotdot")
	}
	if _, err := safeJoin(root, "mods/a.jar"); err != nil {
		t.Fatal(err)
	}
}

func TestRules(t *testing.T) {
	if !rulesAllow(nil, nil) {
		t.Fatal("empty allows")
	}
	rules := []rule{{Action: "allow", OS: &struct {
		Name string `json:"name"`
		Arch string `json:"arch"`
	}{Name: "nope"}}}
	if rulesAllow(rules, nil) {
		t.Fatal("other os must not match")
	}
}

func TestBundledPack(t *testing.T) {
	r, err := zip.NewReader(bytes.NewReader(bundledPack), int64(len(bundledPack)))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range r.File {
		if f.Name == "modrinth.index.json" {
			found = true
		}
	}
	if !found {
		t.Fatal("no index")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", "")
	path, err := bundledMrpackPath()
	if err != nil {
		t.Fatal(err)
	}
	mods, err := listMods(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(mods) < 10 {
		t.Fatalf("mods %d", len(mods))
	}
}

func TestArgFileQuoting(t *testing.T) {
	got := forArgFile(`C:\Users\Maks Alinator\game`)
	if got[0] != '"' {
		t.Fatalf("quote: %s", got)
	}
}

func TestLaunchPlanNoDirectJoin(t *testing.T) {
	dir := t.TempDir()
	id := "1.21.1"
	verDir := filepath.Join(dir, "versions", id)
	if err := os.MkdirAll(verDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonBody := `{
	  "id": "1.21.1",
	  "mainClass": "net.minecraft.client.main.Main",
	  "assetIndex": {"id": "17"},
	  "libraries": [
	    {"name": "demo:demo:1", "downloads": {"artifact": {"path": "demo/demo/1/demo-1.jar", "sha1": "", "url": ""}}},
	    {"name": "org.lwjgl:lwjgl:3.3.3:natives-linux", "downloads": {"artifact": {"path": "org/lwjgl/lwjgl/3.3.3/lwjgl-3.3.3-natives-linux.jar", "sha1": "", "url": ""}}},
	    {"name": "org.lwjgl:lwjgl:3.3.3:natives-windows-arm64", "downloads": {"artifact": {"path": "org/lwjgl/lwjgl/3.3.3/lwjgl-3.3.3-natives-windows-arm64.jar", "sha1": "", "url": ""}}},
	    {"name": "cpw.mods:bootstraplauncher:2.0.2", "downloads": {"artifact": {"path": "cpw/mods/bootstraplauncher/2.0.2/bootstraplauncher-2.0.2.jar", "sha1": "", "url": ""}}}
	  ],
	  "arguments": {
	    "jvm": [
	      "-Djava.library.path=${natives_directory}",
	      "-cp", "${classpath}",
	      "-DignoreList=bootstraplauncher-2.0.2.jar",
	      "-p", "${library_directory}/cpw/mods/bootstraplauncher/2.0.2/bootstraplauncher-2.0.2.jar"
	    ],
	    "game": [
	      "--uuid", "${auth_uuid}",
	      {"rules": [{"action": "allow", "features": {"has_quick_plays_support": true}}], "value": ["--quickPlayPath", "${quickPlayPath}"]},
	      {"rules": [{"action": "allow", "features": {"is_quick_play_multiplayer": true}}], "value": ["--quickPlayMultiplayer", "${quickPlayMultiplayer}"]}
	    ]
	  }
	}`
	if err := os.WriteFile(filepath.Join(verDir, id+".json"), []byte(jsonBody), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(dir, "libraries", "demo", "demo", "1")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, "demo-1.jar"), []byte("jar"), 0o644); err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(dir, "libraries", "org", "lwjgl", "lwjgl", "3.3.3")
	if err := os.MkdirAll(native, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeTinyZip(filepath.Join(native, "lwjgl-3.3.3-natives-linux.jar")); err != nil {
		t.Fatal(err)
	}
	boot := filepath.Join(dir, "libraries", "cpw", "mods", "bootstraplauncher", "2.0.2")
	if err := os.MkdirAll(boot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(boot, "bootstraplauncher-2.0.2.jar"), []byte("PK"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(verDir, id+".jar"), []byte("client"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, err := buildPlan(launchOpts{
		GameDir: dir, VersionID: id, Profile: profile{Name: "Steve", ID: "0123456789abcdef0123456789abcdef", XUID: "1"},
		AccessToken: "token", RamMB: 4096, Width: 1280, Height: 720, Host: "202.181.188.169", Port: 25841,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, a := range args {
		joined += a + "\n"
	}
	if bytes.Contains([]byte(joined), []byte("quickPlay")) || bytes.Contains([]byte(joined), []byte("202.181.188.169")) {
		t.Fatalf("direct join leaked:\n%s", joined)
	}
	cp := ""
	for i, a := range args {
		if a == "-cp" && i+1 < len(args) {
			cp = args[i+1]
		}
	}
	if bytes.Contains([]byte(cp), []byte("natives-")) || bytes.Contains([]byte(cp), []byte("bootstraplauncher")) {
		t.Fatalf("classpath:\n%s", cp)
	}
	if !bytes.Contains([]byte(cp), []byte("demo-1.jar")) {
		t.Fatal(cp)
	}
	if !bytes.Contains([]byte(joined), []byte("01234567-89ab-cdef-0123-456789abcdef")) {
		t.Fatal("uuid not dashed")
	}
	if !bytes.Contains([]byte(joined), []byte("-Xmx4096M")) {
		t.Fatal("ram")
	}
}

func writeTinyZip(path string) error {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("glfw.dll")
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte("x")); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func TestStripClasspath(t *testing.T) {
	sep := string(os.PathListSeparator)
	cp := "libs/demo.jar" + sep + "libs/lwjgl-natives-linux.jar" + sep + "libs/bootstraplauncher-2.0.2.jar" + sep + "client.jar"
	args := []string{
		"-cp", cp,
		"-DignoreList=bootstraplauncher-2.0.2.jar",
		"-p", "libs/bootstraplauncher-2.0.2.jar",
	}
	got := stripClasspath(args, sep)
	out := got[1]
	if bytes.Contains([]byte(out), []byte("natives-")) || bytes.Contains([]byte(out), []byte("bootstraplauncher")) {
		t.Fatal(out)
	}
	if !bytes.Contains([]byte(out), []byte("demo.jar")) || !bytes.Contains([]byte(out), []byte("client.jar")) {
		t.Fatal(out)
	}
}

func TestNativeClassifier(t *testing.T) {
	if !nativeClassifierOK("org.lwjgl:lwjgl:3.3.3") {
		t.Fatal("plain lib")
	}
	okWin := nativeClassifierOK("org.lwjgl:lwjgl-glfw:3.3.3:natives-windows")
	okArm := nativeClassifierOK("org.lwjgl:lwjgl-glfw:3.3.3:natives-windows-arm64")
	okX86 := nativeClassifierOK("org.lwjgl:lwjgl-glfw:3.3.3:natives-windows-x86")
	if osName() == "linux" {
		if okWin || okArm || okX86 {
			t.Fatal("windows natives must not load on linux")
		}
		if !nativeClassifierOK("org.lwjgl:lwjgl-glfw:3.3.3:natives-linux") {
			t.Fatal("linux amd64 natives")
		}
	}
}

func TestDashedUUID(t *testing.T) {
	got := dashedUUID("0123456789abcdef0123456789abcdef")
	if got != "01234567-89ab-cdef-0123-456789abcdef" {
		t.Fatal(got)
	}
}

func TestExceptionLines(t *testing.T) {
	text := "noise\njava.lang.IllegalStateException: Module named org.lwjgl was already loaded\n\tat cpw.mods.bootstraplauncher.BootstrapLauncher.run(BootstrapLauncher.java:210)\nCaused by: java.lang.module.ResolutionException: split\n"
	lines := exceptionLines(text)
	if len(lines) != 2 {
		t.Fatal(lines)
	}
	if !bytes.Contains([]byte(lines[0]), []byte("Module named org.lwjgl")) {
		t.Fatal(lines)
	}
}
