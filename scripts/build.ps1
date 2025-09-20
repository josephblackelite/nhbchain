$ErrorActionPreference = "Stop"
Write-Host "[*] go mod tidy"
go mod tidy
Write-Host "[*] building..."
go build -trimpath -ldflags="-s -w" -buildvcs=false -o bin/nhb.exe ./cmd/nhb
Write-Host "[✓] done: bin/nhb.exe"
