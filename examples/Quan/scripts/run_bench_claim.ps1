param(
    [Parameter(Mandatory = $true)]
    [string]$Scenario,

    [Parameter(Mandatory = $true)]
    [int64]$CouponId,

    [Parameter(Mandatory = $true)]
    [int64]$StartUserId,

    [string]$BaseUrl = "http://127.0.0.1:8081",
    [int]$Requests = 6000,
    [int]$Concurrency = 80,
    [string]$UserMode = "unique",
    [int]$UserPool = 200,
    [string]$IdemMode = "unique",
    [string]$IdemPrefix = "bench",
    [timespan]$Timeout = "00:00:03",
    [string]$ReportDir = "examples/Quan/artifacts/bench",

    [switch]$ResetData,
    [string]$MySQLDsn = "root:root@tcp(127.0.0.1:3306)/mini_jupiter?parseTime=true&loc=Local&charset=utf8mb4",
    [int]$Stock = 20000,
    [int]$PerUserLimit = 1
)

$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..\..")).Path
Push-Location $repoRoot
try {
    New-Item -ItemType Directory -Path $ReportDir -Force | Out-Null
    $stamp = Get-Date -Format "yyyyMMdd_HHmmss"
    $reportOut = Join-Path $ReportDir "${Scenario}_${CouponId}_${StartUserId}_${stamp}.json"

    if ($ResetData) {
        Write-Host "Resetting benchmark data for coupon_id=$CouponId"
        go run ./examples/Quan/bench/cmd/benchprep `
            -dsn "$MySQLDsn" `
            -coupon-id $CouponId `
            -stock $Stock `
            -per-user-limit $PerUserLimit `
            -campaign-name "request_${Scenario}_$stamp"
    }

    Write-Host "Running benchmark scenario=$Scenario coupon_id=$CouponId start_user_id=$StartUserId"
    go run ./examples/Quan/bench/cmd/benchclaim `
        -scenario $Scenario `
        -base-url $BaseUrl `
        -coupon-id $CouponId `
        -requests $Requests `
        -concurrency $Concurrency `
        -user-mode $UserMode `
        -user-pool $UserPool `
        -start-user-id $StartUserId `
        -idem-mode $IdemMode `
        -idem-prefix $IdemPrefix `
        -timeout $Timeout `
        -report-out $reportOut

    Write-Host "Benchmark result saved: $reportOut"
}
finally {
    Pop-Location
}
