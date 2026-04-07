param(
    [int64]$CouponId = 9916,
    [int]$Stock = 10000,
    [int]$PerUserLimit = 1,
    [int]$Requests = 1000000,
    [int]$Concurrency = 500,
    [int64]$StartUserId = 100000
)

$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..\..")).Path
$reportDir = "examples/Quan/artifacts/bench"
$reportOut = Join-Path $repoRoot "$reportDir\headline_${CouponId}_$(Get-Date -Format 'yyyyMMdd_HHmmss').json"
$quanProc = $null

function Wait-HttpOk([string]$Url, [int]$TimeoutSeconds = 60) {
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        try {
            $resp = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 3
            if ($resp.StatusCode -eq 200) {
                return
            }
        } catch {
        }
        Start-Sleep -Milliseconds 500
    }
    throw "Timed out waiting for $Url"
}

Push-Location $repoRoot
try {
    powershell -ExecutionPolicy Bypass -File .\examples\Quan\scripts\start_infra.ps1

    $quanProc = Start-Process -FilePath "powershell" -ArgumentList @(
        "-ExecutionPolicy", "Bypass",
        "-File", ".\examples\Quan\scripts\start_quan.ps1",
        "-ConfigPath", "examples/Quan/config.sample.yaml"
    ) -PassThru

    Wait-HttpOk "http://127.0.0.1:8081/ping" 90

    go run ./examples/Quan/bench/cmd/benchprep `
        -dsn "root:root@tcp(127.0.0.1:3306)/mini_jupiter?parseTime=true&loc=Local&charset=utf8mb4" `
        -coupon-id $CouponId `
        -stock $Stock `
        -per-user-limit $PerUserLimit `
        -campaign-name "headline_repro"

    New-Item -ItemType Directory -Path $reportDir -Force | Out-Null

    go run ./examples/Quan/bench/cmd/benchclaim `
        -scenario "headline_high_conflict" `
        -base-url "http://127.0.0.1:8081" `
        -coupon-id $CouponId `
        -requests $Requests `
        -concurrency $Concurrency `
        -user-mode "unique" `
        -start-user-id $StartUserId `
        -idem-mode "unique" `
        -idem-prefix "headline" `
        -timeout "3s" `
        -report-out $reportOut

    go run ./examples/Quan/bench/cmd/benchaudit `
        -dsn "root:root@tcp(127.0.0.1:3306)/mini_jupiter?parseTime=true&loc=Local&charset=utf8mb4" `
        -coupon-id $CouponId `
        -report-path $reportOut `
        -redis-enabled `
        -redis-mode "sentinel" `
        -redis-addrs "127.0.0.1:26379,127.0.0.1:26380,127.0.0.1:26381" `
        -redis-master-name "mymaster" `
        -request-prefix "quan:claim"

    Write-Host "Headline reproduction report: $reportOut"
}
finally {
    if ($null -ne $quanProc -and -not $quanProc.HasExited) {
        Stop-Process -Id $quanProc.Id -Force
    }
    Pop-Location
}
