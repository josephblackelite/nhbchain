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
$env:NHB_VALIDATOR_PASS = "devpassphrase"
$env:NHB_RPC_JWT_SECRET = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
$env:NHB_ALLOW_AUTOGENESIS = "true"

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
