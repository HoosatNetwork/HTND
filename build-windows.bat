@echo off
cd /d "%~dp0"
go build -o htnd.exe .
htnd.exe --version
