param(
    [string]$ConfigPath = "examples/Quan/config.sample.yaml",
    [string]$BaseUrl = "http://127.0.0.1:8081",
    [string]$RedisAddr = "127.0.0.1:6379",
    [int]$Port = 8081,
    [string]$PingPath = "/ping",
    [string]$MySQLDsn = "root:root@tcp(127.0.0.1:3306)/mini_jupiter?parseTime=true&loc=Local&charset=utf8mb4",
    [long]$BaseCouponId = 9900,
    [int]$PerUserLimit = 1,
    [int]$RequestsPerStep = 20000,
    [string]$ConcurrencyLevels = "100,200,400,800,1200",
    [long]$StartUserId = 1000000,
    [int]$DurationSeconds = 10,
    [int]$RequestTimeoutSeconds = 15,
    [string]$OutputDir = "",
    [switch]$StopOnFirstFailure,
    [switch]$StartDockerInfra
)

$ErrorActionPreference = "Stop"

function New-RunStamp {
    return Get-Date -Format "yyyyMMdd_HHmmss"
}

function Ensure-Directory {
    param([string]$Path)
    if (-not (Test-Path $Path)) {
        New-Item -ItemType Directory -Path $Path -Force | Out-Null
    }
}

function Resolve-RepoPath {
    param(
        [string]$RepoRoot,
        [string]$Path
    )

    if ([System.IO.Path]::IsPathRooted($Path)) {
        return [System.IO.Path]::GetFullPath($Path)
    }
    return [System.IO.Path]::GetFullPath((Join-Path $RepoRoot $Path))
}

function To-ContainerPath {
    param(
        [string]$RepoRoot,
        [string]$Path
    )

    $repoUri = New-Object System.Uri(($RepoRoot.TrimEnd("\") + "\"))
    $pathUri = New-Object System.Uri($Path)
    $relative = [System.Uri]::UnescapeDataString($repoUri.MakeRelativeUri($pathUri).ToString())
    return "/work/" + ($relative -replace "/", "/")
}

function Wait-QuanReady {
    param(
        [string]$Url,
        [int]$TimeoutSeconds
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        try {
            $resp = Invoke-WebRequest -UseBasicParsing $Url -TimeoutSec 2
            if ($resp.StatusCode -eq 200 -and $resp.Content -match "pong") {
                return
            }
        }
        catch {
        }
        Start-Sleep -Milliseconds 500
    }
    throw "Quan server did not become ready at $Url within ${TimeoutSeconds}s"
}

function New-VegetaTargets {
    param(
        [string]$Path,
        [string]$BaseUrl,
        [long]$CouponId,
        [long]$StartUserId,
        [int]$RequestCount,
        [string]$IdemPrefix
    )

    $targetUrl = $BaseUrl.TrimEnd("/") + "/api/v1/coupons/$CouponId/claim"
    $writer = New-Object System.IO.StreamWriter($Path, $false, (New-Object System.Text.UTF8Encoding($false)))
    for ($idx = 0; $idx -lt $RequestCount; $idx++) {
        $userId = $StartUserId + $idx
        $idemKey = "${IdemPrefix}_${idx}"
        $target = [ordered]@{
            method = "POST"
            url = $targetUrl
            header = [ordered]@{
                "X-User-ID" = @("$userId")
                "Idempotency-Key" = @("$idemKey")
            }
        } | ConvertTo-Json -Compress -Depth 4
        $writer.WriteLine($target)
    }
    $writer.Dispose()
}

function Read-StatusSummary {
    param([string]$Path)

    $statusCounts = @{}
    $transportErrors = 0
    $total = 0

    foreach ($line in Get-Content $Path) {
        if ([string]::IsNullOrWhiteSpace($line)) {
            continue
        }
        $total++
        $item = $line | ConvertFrom-Json
        if ($item.error) {
            $transportErrors++
        }
        $code = [string]$item.code
        if (-not [string]::IsNullOrWhiteSpace($code)) {
            if (-not $statusCounts.ContainsKey($code)) {
                $statusCounts[$code] = 0
            }
            $statusCounts[$code]++
        }
    }

    $serverErrors = 0
    $clientErrors = 0
    $success = 0
    foreach ($key in $statusCounts.Keys) {
        if ($key -match "^2\d\d$") {
            $success += $statusCounts[$key]
        }
        elseif ($key -match "^4\d\d$") {
            $clientErrors += $statusCounts[$key]
        }
        elseif ($key -match "^5\d\d$") {
            $serverErrors += $statusCounts[$key]
        }
    }

    return [pscustomobject]@{
        status_counts = [ordered]@{} + $statusCounts
        total = $total
        success = $success
        client_errors = $clientErrors
        server_errors = $serverErrors
        transport_errors = $transportErrors
    }
}

$repoRoot = Split-Path $PSScriptRoot -Parent
$stamp = New-RunStamp
if (-not $OutputDir) {
    $OutputDir = "codex/tmp/capacity_${stamp}"
}
$outputDirAbs = Resolve-RepoPath -RepoRoot $repoRoot -Path $OutputDir
Ensure-Directory $outputDirAbs

if ($StartDockerInfra) {
    try {
        Push-Location $repoRoot
        docker compose up -d mysql redis | Out-Null
    }
    catch {
        Write-Warning "docker compose up failed; continuing with currently reachable MySQL/Redis"
    }
    finally {
        Pop-Location
    }
}

if ([string]::IsNullOrWhiteSpace($ConcurrencyLevels)) {
    throw "ConcurrencyLevels must contain at least one value"
}
if ($RequestsPerStep -le 0) {
    throw "RequestsPerStep must be > 0"
}
if ($DurationSeconds -le 0) {
    throw "DurationSeconds must be > 0"
}

$levelValues = @()
foreach ($rawLevel in ($ConcurrencyLevels -split ",")) {
    $trimmed = $rawLevel.Trim()
    if ($trimmed -eq "") {
        continue
    }
    $parsed = 0
    if (-not [int]::TryParse($trimmed, [ref]$parsed) -or $parsed -le 0) {
        throw "invalid concurrency level: $trimmed"
    }
    $levelValues += $parsed
}
if ($levelValues.Count -eq 0) {
    throw "ConcurrencyLevels must contain at least one valid integer"
}

$stopScript = Join-Path $PSScriptRoot "quan-stop.ps1"
$auditScript = Join-Path $PSScriptRoot "quan-audit-ledger.ps1"
$mountSpec = "${repoRoot}:/work"
$containerBaseUrl = $BaseUrl -replace "127\.0\.0\.1", "host.docker.internal" -replace "localhost", "host.docker.internal"
$summaryPath = Join-Path $outputDirAbs "summary.json"
$stdoutLog = Join-Path $outputDirAbs "quan.out.log"
$stderrLog = Join-Path $outputDirAbs "quan.err.log"
$pidFile = Join-Path $outputDirAbs "quan.pid"

$previousConfig = $env:CONFIG_PATH
$proc = $null
$levels = New-Object System.Collections.Generic.List[object]
$maxPassedConcurrency = 0
$firstFailingConcurrency = $null
$hasCorrectnessFailure = $false

& $stopScript -Port $Port -PidFile $pidFile -KillPortOwner -Quiet

try {
    $env:CONFIG_PATH = $ConfigPath
    Push-Location $repoRoot
    $proc = Start-Process -FilePath "go" -ArgumentList @("run", "./examples/Quan") -PassThru -RedirectStandardOutput $stdoutLog -RedirectStandardError $stderrLog
    Set-Content -Path $pidFile -Value $proc.Id

    Wait-QuanReady -Url ($BaseUrl.TrimEnd("/") + $PingPath) -TimeoutSeconds 20

    for ($idx = 0; $idx -lt $levelValues.Count; $idx++) {
        $concurrency = [int]$levelValues[$idx]
        $couponId = $BaseCouponId + $idx
        $levelDir = Join-Path $outputDirAbs ("c" + $concurrency)
        Ensure-Directory $levelDir

        $targetsAbs = Join-Path $levelDir "targets.jsonl"
        $resultBinAbs = Join-Path $levelDir "result.bin"
        $reportAbs = Join-Path $levelDir "report.json"
        $encodedAbs = Join-Path $levelDir "results.jsonl"
        $auditAbs = Join-Path $levelDir "audit.json"

        $campaignName = "capacity_c${concurrency}_${stamp}"
        $levelStartUserId = $StartUserId + ($idx * $RequestsPerStep)
        $idemPrefix = "capacity_c${concurrency}_${stamp}"

        go run ./examples/Quan/bench/cmd/benchprep `
            -dsn $MySQLDsn `
            -redis-addr $RedisAddr `
            -coupon-id $couponId `
            -stock $RequestsPerStep `
            -per-user-limit $PerUserLimit `
            -campaign-name $campaignName

        New-VegetaTargets `
            -Path $targetsAbs `
            -BaseUrl $containerBaseUrl `
            -CouponId $couponId `
            -StartUserId $levelStartUserId `
            -RequestCount $RequestsPerStep `
            -IdemPrefix $idemPrefix

        $attackArgs = @(
            "run", "--rm",
            "-v", $mountSpec,
            "-w", "/work",
            "peterevans/vegeta",
            "vegeta", "attack",
            "-format=json",
            "-targets", (To-ContainerPath -RepoRoot $repoRoot -Path $targetsAbs),
            "-output", (To-ContainerPath -RepoRoot $repoRoot -Path $resultBinAbs),
            "-workers", "$concurrency",
            "-max-workers", "$concurrency",
            "-connections", "$concurrency",
            "-max-connections", "$concurrency",
            "-rate", "0",
            "-duration", ("{0}s" -f $DurationSeconds),
            "-timeout", ("{0}s" -f $RequestTimeoutSeconds),
            "-keepalive=true",
            "-name", ("quan_capacity_c{0}" -f $concurrency)
        )
        & docker @attackArgs
        if ($LASTEXITCODE -ne 0) {
            throw "vegeta attack failed at concurrency $concurrency"
        }

        $reportJson = & docker run --rm -v $mountSpec -w /work peterevans/vegeta vegeta report -type=json (To-ContainerPath -RepoRoot $repoRoot -Path $resultBinAbs)
        if ($LASTEXITCODE -ne 0) {
            throw "vegeta report failed at concurrency $concurrency"
        }
        $reportJson | Set-Content -Path $reportAbs -Encoding utf8

        & docker run --rm -v $mountSpec -w /work peterevans/vegeta vegeta encode -to=json (To-ContainerPath -RepoRoot $repoRoot -Path $resultBinAbs) | Set-Content -Path $encodedAbs -Encoding utf8
        if ($LASTEXITCODE -ne 0) {
            throw "vegeta encode failed at concurrency $concurrency"
        }

        $report = Get-Content $reportAbs -Raw | ConvertFrom-Json
        $statusSummary = Read-StatusSummary -Path $encodedAbs
        $auditRaw = & $auditScript -CouponId $couponId -MySQLDsn $MySQLDsn -OutputPath $auditAbs
        $audit = $auditRaw | ConvertFrom-Json

        $reasons = New-Object System.Collections.Generic.List[string]
        if (-not $audit.verdict.pass) {
            $hasCorrectnessFailure = $true
            foreach ($reason in $audit.verdict.reasons) {
                [void]$reasons.Add("ledger: $reason")
            }
        }
        if ($statusSummary.transport_errors -gt 0) {
            [void]$reasons.Add("transport errors: $($statusSummary.transport_errors)")
        }
        if ($statusSummary.server_errors -gt 0) {
            [void]$reasons.Add("server errors: $($statusSummary.server_errors)")
        }
        if ($statusSummary.client_errors -gt 0) {
            [void]$reasons.Add("client errors: $($statusSummary.client_errors)")
        }
        if ($report.requests -gt $RequestsPerStep) {
            [void]$reasons.Add("unique request pool exhausted: sent $($report.requests) > pool $RequestsPerStep")
        }
        if ($statusSummary.success -ne $statusSummary.total) {
            [void]$reasons.Add("success responses $($statusSummary.success) != observed responses $($statusSummary.total)")
        }
        if ($audit.claims.persisted_claims -ne $statusSummary.success) {
            [void]$reasons.Add("persisted claims $($audit.claims.persisted_claims) != success responses $($statusSummary.success)")
        }

        $pass = $reasons.Count -eq 0
        if ($pass) {
            $maxPassedConcurrency = $concurrency
        }
        elseif ($null -eq $firstFailingConcurrency) {
            $firstFailingConcurrency = $concurrency
        }

        $levels.Add([pscustomobject]@{
            concurrency = $concurrency
            coupon_id = $couponId
            unique_request_pool = $RequestsPerStep
            duration_seconds = $DurationSeconds
            requests_sent = $report.requests
            tool = "vegeta"
            throughput = $report.throughput
            rate = $report.rate
            success_ratio = $report.success
            latency_ms = [ordered]@{
                p50 = [math]::Round(($report.latencies.PSObject.Properties["50th"].Value / 1000000.0), 2)
                p95 = [math]::Round(($report.latencies.PSObject.Properties["95th"].Value / 1000000.0), 2)
                p99 = [math]::Round(($report.latencies.PSObject.Properties["99th"].Value / 1000000.0), 2)
                max = [math]::Round(($report.latencies.max / 1000000.0), 2)
            }
            status_counts = $statusSummary.status_counts
            transport_errors = $statusSummary.transport_errors
            ledger_audit_path = $auditAbs
            ledger_pass = [bool]$audit.verdict.pass
            pass = $pass
            failure_reasons = @($reasons)
        })

        if (-not $pass -and $StopOnFirstFailure) {
            break
        }
    }
}
finally {
    if ($null -ne $previousConfig) {
        $env:CONFIG_PATH = $previousConfig
    }
    else {
        Remove-Item Env:CONFIG_PATH -ErrorAction SilentlyContinue
    }

    Pop-Location -ErrorAction SilentlyContinue
    & $stopScript -Port $Port -PidFile $pidFile -KillPortOwner -Quiet
}

$summary = [pscustomobject]@{
    scenario = "capacity_scan"
    tool = "vegeta (docker)"
    checked_at = (Get-Date).ToUniversalTime().ToString("o")
    base_url = $BaseUrl
    unique_request_pool = $RequestsPerStep
    duration_seconds = $DurationSeconds
    concurrency_levels = $levelValues
    max_passed_concurrency = $maxPassedConcurrency
    first_failing_concurrency = $firstFailingConcurrency
    failure_rule = @(
        "all observed responses must be 200",
        "transport errors must be 0",
        "ledger audit must pass",
        "persisted claim count must equal success responses",
        "observed requests must not exceed the unique request pool"
    )
    levels = @($levels.ToArray())
    stdout_log = $stdoutLog
    stderr_log = $stderrLog
}

$summaryJson = $summary | ConvertTo-Json -Depth 8
$summaryJson | Set-Content -Path $summaryPath -Encoding utf8
$summaryJson

if ($hasCorrectnessFailure) {
    exit 2
}
