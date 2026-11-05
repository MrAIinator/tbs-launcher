@echo off
chcp 65001 >nul
cd /d "%~dp0"
set "SRC="
if exist "%~dp0updates\pending\main.js" set "SRC=%~dp0updates\pending"
if exist "%~dp0updates\pending\launcher\main.js" set "SRC=%~dp0updates\pending\launcher"
if defined SRC (
  echo Ставлю обновление лаунчера...
  xcopy /E /Y /I "%SRC%\lib" "%~dp0lib" >nul
  xcopy /E /Y /I "%SRC%\ui" "%~dp0ui" >nul
  if exist "%SRC%\node_modules\" xcopy /E /Y /I "%SRC%\node_modules" "%~dp0node_modules" >nul
  if exist "%SRC%\pack\pack.mrpack" xcopy /E /Y /I "%SRC%\pack" "%~dp0pack" >nul
  copy /Y "%SRC%\main.js" "%~dp0main.js" >nul
  copy /Y "%SRC%\manifest.json" "%~dp0manifest.json" >nul
  copy /Y "%SRC%\package.json" "%~dp0package.json" >nul
  rmdir /S /Q "%~dp0updates\pending"
)
set "NODE=%~dp0runtime\node.exe"
if not exist "%NODE%" set "NODE=node"
where "%NODE%" >nul 2>&1
if errorlevel 1 if not exist "%~dp0runtime\node.exe" (
  echo Нет runtime\node.exe и Node.js в системе. Скачай архив лаунчера целиком.
  pause
  exit /b 1
)
echo TBS Launcher. Окно откроется отдельно. Консоль не закрывай, пока идёт первая установка.
"%NODE%" "%~dp0main.js"
echo Лаунчер закрыт.
pause
