# NHBCoin Local Node Startup Script
# Run this script in a dedicated PowerShell window to keep the node running in the background.

Write-Host "Initializing NHBCoin Local Development Node..." -ForegroundColor Cyan

# 0. Ensure no existing node is running
Write-Host "Stopping any running background nodes..." -ForegroundColor Yellow
$nodeProcess = Get-Process nhb-node -ErrorAction SilentlyContinue
if ($nodeProcess) {
    Stop-Process -Name "nhb-node" -Force
    # Wait a moment for file locks to be fully released
    Start-Sleep -Seconds 2
}

# 1. Ensure the executable exists
if (!(Test-Path -Path ".\nhb-node.exe")) {
    Write-Host "Compiling nhb-node.exe..." -ForegroundColor Yellow
    go build -o nhb-node.exe ./cmd/nhb/
    if ($LASTEXITCODE -ne 0) {
        Write-Host "Failed to compile the node. Please ensure Go is installed." -ForegroundColor Red
        exit
    }
}

# 2. Set the environment variables required for the local network
$env:NHB_ENV = "local"
$env:NHB_ALLOW_AUTOGENESIS = "true"
$env:NHB_MASTER_TREASURY = "nhb138a8dk8nwq4hurvqwdde3mmxj6sf5pwz78h0q4"

# Generate secrets at runtime rather than hardcoding static values -- a
# static, previously-published value can become load-bearing if this script
# pattern is ever reused elsewhere or the local port is exposed. Matches the
# generate-if-unset pattern already used by nhb-go.sh. Set NHB_VALIDATOR_PASS
# / NHB_RPC_JWT_SECRET yourself beforehand to override.
if (-not $env:NHB_VALIDATOR_PASS) {
    $passBytes = New-Object byte[] 18
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($passBytes)
    $env:NHB_VALIDATOR_PASS = [Convert]::ToBase64String($passBytes)
}
if (-not $env:NHB_RPC_JWT_SECRET) {
    $secretBytes = New-Object byte[] 32
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($secretBytes)
    $env:NHB_RPC_JWT_SECRET = -join ($secretBytes | ForEach-Object { $_.ToString("x2") })
    Write-Host "[i] Generated a new local RPC JWT secret (not persisted -- set NHB_RPC_JWT_SECRET yourself to reuse one across restarts)." -ForegroundColor DarkGray
}

# 3. Clean up any previous state to prevent genesis mismatches across restarts
Write-Host "Clearing old chain data..." -ForegroundColor DarkGray
if (Test-Path -Path "nhb-data-local") {
    Remove-Item -Recurse -Force nhb-data-local
}

Write-Host "Starting node with config-local.toml on port 8081..." -ForegroundColor Green
Write-Host "Keep this window open. Do not close it unless you want to stop the node." -ForegroundColor Yellow
Write-Host "------------------------------------------------------------------"

# 4. Start the node
.\nhb-node.exe --config config-local.toml
