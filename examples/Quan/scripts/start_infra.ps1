param(
    [switch]$WithObservability
)

$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..\..")).Path
Push-Location $repoRoot
try {
    $services = @(
        "mysql",
        "rabbitmq",
        "redis-master",
        "redis-replica",
        "redis-sentinel-1",
        "redis-sentinel-2",
        "redis-sentinel-3"
    )
    if ($WithObservability) {
        $services += @("prometheus", "grafana")
    }

    Write-Host "Starting infra services: $($services -join ', ')"
    docker compose up -d @services
    docker compose ps
    Write-Host "Infra ready. Next: run start_quan.ps1"
}
finally {
    Pop-Location
}
