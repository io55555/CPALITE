@echo off
setlocal
REM Requires MinGW gcc on PATH (or set MINGW below).
if defined MINGW (
  set "PATH=%MINGW%;%PATH%"
  set "CC=%MINGW%\gcc.exe"
)
if not exist dist mkdir dist
set CGO_ENABLED=1
set VERSION=1.3.7
echo Building dist\grok-manager.dll (v%VERSION%) ...
go build -buildvcs=false -buildmode=c-shared -trimpath -ldflags="-s -w" -o dist\grok-manager.dll .
if errorlevel 1 (
  echo BUILD FAILED
  exit /b 1
)
copy /y dist\grok-manager.dll dist\grok-manager-windows-amd64.dll >nul
copy /y dist\grok-manager.dll dist\grok-manager-v%VERSION%.dll >nul
echo OK
dir dist\grok-manager*.dll
endlocal
