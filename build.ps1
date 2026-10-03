$env:GOTOOLCHAIN = "go1.27.1"
Set-Location $PSScriptRoot
go build -tags pebblegozstd -o htnd.exe .
go build -tags pebblegozstd -o htnctl.exe ./cmd/htnctl
