$ErrorActionPreference = "Stop"

$goVersion = if ($env:GO_VERSION) { $env:GO_VERSION } else { "1.22.6" }
$goCmd = if ($env:GO_CMD) { $env:GO_CMD } else { $null }

if (-not $goCmd) {
    $candidate = "go$goVersion"
    if (Get-Command $candidate -ErrorAction SilentlyContinue) {
        $goCmd = $candidate
    }
    else {
        $goCmd = "go"
        if (-not $env:GOTOOLCHAIN) {
            $env:GOTOOLCHAIN = "go$goVersion"
        }
    }
}

if (-not $env:GOFLAGS) {
    $env:GOFLAGS = "-buildvcs=false"
}

Write-Host "[*] using $goCmd (target Go $goVersion)"
& $goCmd version
Write-Host "[*] go mod tidy"
& $goCmd mod tidy
Write-Host "[*] building..."
& $goCmd build -trimpath -ldflags="-s -w" -buildvcs=false -o bin/nhb.exe ./cmd/nhb
Write-Host "[✓] done: bin/nhb.exe"
