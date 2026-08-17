@echo off
rem SPDX-License-Identifier: AGPL-3.0-or-later
rem
rem Starts the monitor and opens the panel.
rem
rem The pause at the end is deliberate: double-clicked from Explorer, a console
rem program closes its window the moment it stops, taking the reason with it.
rem Without the pause the first-run password and any startup error are gone
rem before anybody reads them.
setlocal
cd /d "%~dp0"
wbmon.exe %*
if errorlevel 1 (
  echo.
  echo wbmon завершился с ошибкой. Строки выше — что случилось.
  pause
)
endlocal
