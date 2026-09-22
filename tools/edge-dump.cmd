@echo off
rem Dumps the DOM of a URL with headless Edge, for testing the system
rem proxy registration from the Makefile. Edge writes nothing to a
rem PowerShell pipeline, so this runs from cmd with file redirects.
"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe" --headless=new --disable-gpu --dump-dom --user-data-dir=%TEMP%\tswipoexp-edge-profile --no-first-run %1 > %~dp0edge-out.txt 2> %~dp0edge-err.txt
