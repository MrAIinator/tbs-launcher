TBS Launcher 1.1.2 — исходник.

Сборка Windows exe (Go 1.23+):

  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w -H windowsgui" -o TBS-Launcher.exe .

Нужны файлы рядом: ui/, pack/pack.mrpack, manifest.json, rsrc_windows_amd64.syso (иконка).
Игра ставится в %APPDATA%\TBSLauncher.
Обновления читаются из manifest.json на GitHub.
