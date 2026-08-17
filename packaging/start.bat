@echo off
rem SPDX-License-Identifier: AGPL-3.0-or-later
rem
rem Starts the monitor and opens the panel.
rem
rem The pause at the end is deliberate: double-clicked from Explorer, a console
rem program closes its window the moment it stops, taking the reason with it.
rem Without the pause the first-run password and any startup error are gone
rem before anybody reads them.
rem
rem For everyday use there is wbmon-tray.exe beside this file: the same program
rem with no console window and an icon in the notification area. This script is
rem the one to run the first time, because the first-run password is printed
rem here and only written to a file there.
setlocal
cd /d "%~dp0"
wbmon.exe %*
if errorlevel 1 (
  echo.
  echo wbmon завершился с ошибкой. Строки выше — что случилось.
  pause
)
endlocal
