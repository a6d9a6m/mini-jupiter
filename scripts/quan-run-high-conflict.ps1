param(
    [long]$CouponId = 9801,
    [int]$Stock = 500,
    [int]$PerUserLimit = 1,
    [int]$Requests = 4000,
    [int]$Concurrency = 60,
    [long]$StartUserId = 950000,
    [string]$ConfigPath = "examples/Quan/config.sample.yaml",
    [string]$BaseUrl = "http://127.0.0.1:8081",
    [string]$MySQLDsn = "root:root@tcp(127.0.0.1:3306)/mini_jupiter?parseTime=true&loc=Local&charset=utf8mb4",
    [string]$RedisAddr = "127.0.0.1:6379",
    [string]$ReportOut = "",
    [string]$AuditOut = "",
    [string]$SummaryOut = "",
    [switch]$StartDockerInfra
)

$ErrorActionPreference = "Stop"

if ($StartDockerInfra) {
    try {
        docker compose up -d mysql redis | Out-Null
    }
    catch {
        Write-Warning "docker compose up failed; continuing with currently reachable MySQL/Redis"
    }
}

$stamp = Get-Date -Format "yyyyMMdd_HHmmss"
if (-not $ReportOut) {
    $ReportOut = "examples/Quan/doc/report/high_conflict_${stamp}.json"
}
if (-not $AuditOut) {
    $AuditOut = "codex/tmp/high_conflict_audit_${stamp}.json"
}
if (-not $SummaryOut) {
    $SummaryOut = "codex/tmp/high_conflict_summary_${stamp}.json"
}

$benchScript = Join-Path $PSScriptRoot "quan-run-bench.ps1"
$auditScript = Join-Path $PSScriptRoot "quan-audit-ledger.ps1"

& $benchScript `
    -ConfigPath $ConfigPath `
    -BaseUrl $BaseUrl `
    -RedisAddr $RedisAddr `
    -Scenario "high_conflict" `
    -CouponId $CouponId `
    -Stock $Stock `
    -PerUserLimit $PerUserLimit `
    -CampaignName "high_conflict_${stamp}" `
    -Requests $Requests `
    -Concurrency $Concurrency `
    -UserMode "unique" `
    -UserPool $Requests `
    -StartUserId $StartUserId `
    -IdemMode "unique" `
    -IdemPrefix "high_conflict_${stamp}" `
    -MySQLDsn $MySQLDsn `
    -ReportOut $ReportOut

$auditRaw = & $auditScript -CouponId $CouponId -MySQLDsn $MySQLDsn -ReportPath $ReportOut -OutputPath $AuditOut
$audit = $auditRaw | ConvertFrom-Json
$report = Get-Content $ReportOut -Raw | ConvertFrom-Json

$summary = [ordered]@{
    scenario = "high_conflict"
    checked_at = (Get-Date).ToUniversalTime().ToString("o")
    benchmark = [ordered]@{
        report_path = $ReportOut
        requests = $Requests
        concurrency = $Concurrency
        qps = $report.qps
        p95_ms = $report.latency_ms.p95
        p99_ms = $report.latency_ms.p99
        success = $report.business.success
        conflict = $report.http_status_counts.PSObject.Properties["409"].Value
        transport_errors = (($report.transport_errors.PSObject.Properties | ForEach-Object { [int]$_.Value }) | Measure-Object -Sum).Sum
    }
    ledger_audit = $audit
}

$summaryJson = $summary | ConvertTo-Json -Depth 8
$summaryDir = Split-Path -Parent $SummaryOut
if ($summaryDir -and -not (Test-Path $summaryDir)) {
    New-Item -ItemType Directory -Path $summaryDir -Force | Out-Null
}
$summaryJson | Set-Content -Path $SummaryOut -Encoding utf8
$summaryJson

if (-not $audit.verdict.pass) {
    throw "High-conflict audit failed. See $AuditOut"
}
