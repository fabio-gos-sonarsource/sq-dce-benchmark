@echo off
REM Double-click this on Windows to run the benchmark using the bench.yaml in this folder.
cd /d "%~dp0"
sq-benchmark.exe run
echo.
pause
