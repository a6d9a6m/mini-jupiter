param(
    [string]$ConfigPath = "examples/Quan/config.sample.yaml"
)

$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..\..")).Path
$absConfig = (Resolve-Path (Join-Path $repoRoot $ConfigPath)).Path

Push-Location $repoRoot
try {
    $env:CONFIG_PATH = $ConfigPath
    Write-Host "Starting Quan with CONFIG_PATH=$ConfigPath ($absConfig)"
    go run ./examples/Quan
}
finally {
    Remove-Item Env:CONFIG_PATH -ErrorAction SilentlyContinue
    Pop-Location
}
