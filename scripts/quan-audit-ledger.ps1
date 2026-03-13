param(
    [long]$CouponId,
    [string]$MySQLDsn = "root:root@tcp(127.0.0.1:3306)/mini_jupiter?parseTime=true&loc=Local&charset=utf8mb4",
    [string]$ReportPath = "",
    [string]$OutputPath = ""
)

$ErrorActionPreference = "Stop"

if ($CouponId -le 0) {
    throw "CouponId must be > 0"
}

$args = @(
    "run", "./examples/Quan/bench/cmd/benchaudit",
    "-dsn", $MySQLDsn,
    "-coupon-id", $CouponId
)
if ($ReportPath) {
    $args += @("-report-path", $ReportPath)
}

$raw = & go @args
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}

if ($OutputPath) {
    $dir = Split-Path -Parent $OutputPath
    if ($dir -and -not (Test-Path $dir)) {
        New-Item -ItemType Directory -Path $dir -Force | Out-Null
    }
    $raw | Set-Content -Path $OutputPath -Encoding utf8
}

$raw
