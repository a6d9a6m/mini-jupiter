param(
    [string]$ConfigPath = "examples/Quan/config.sample.yaml",
    [string]$BaseUrl = "http://127.0.0.1:8081",
    [string]$RedisAddr = "127.0.0.1:6379",
    [int]$Port = 8081,
    [string]$PingPath = "/ping",
    [string]$MySQLDsn = "root:root@tcp(127.0.0.1:3306)/mini_jupiter?parseTime=true&loc=Local&charset=utf8mb4",
    [long]$CouponId = 9860,
    [int]$Stock = 500,
    [int]$PerUserLimit = 1,
    [int]$TotalRequests = 10000,
    [int]$Rate = 10000,
    [int]$DurationSeconds = 1,
    [int]$RequestTimeoutSeconds = 5,
    [int]$Workers = 1000,
    [int]$MaxWorkers = 10000,
    [int]$Connections = 1000,
    [long]$StartUserId = 3000000,
    [string]$TierSpec = "2x2500,10x200,3000x1",
    [int]$Seed = 42,
    [string]$OutputDir = "",
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
        Start-Sleep -Milliseconds 250
    }
    throw "Quan server did not become ready at $Url within ${TimeoutSeconds}s"
}

function Parse-TierSpec {
    param(
        [string]$Spec,
        [int]$ExpectedRequests
    )

    if ([string]::IsNullOrWhiteSpace($Spec)) {
        throw "TierSpec must not be empty"
    }

    $tiers = New-Object System.Collections.Generic.List[object]
    $total = 0
    foreach ($part in ($Spec -split ",")) {
        $trimmed = $part.Trim()
        if ($trimmed -eq "") {
            continue
        }
        if ($trimmed -notmatch "^(?<users>\d+)x(?<requests>\d+)$") {
            throw "invalid TierSpec segment: $trimmed, expected format '<user_count>x<requests_each>'"
        }
        $userCount = [int]$Matches["users"]
        $requestsEach = [int]$Matches["requests"]
        if ($userCount -le 0 -or $requestsEach -le 0) {
            throw "invalid TierSpec segment: $trimmed, counts must be > 0"
        }
        $tierRequests = $userCount * $requestsEach
        $total += $tierRequests
        $tiers.Add([pscustomobject]@{
            user_count = $userCount
            requests_each = $requestsEach
            tier_requests = $tierRequests
        })
    }

    if ($tiers.Count -eq 0) {
        throw "TierSpec produced no tiers"
    }
    if ($total -ne $ExpectedRequests) {
        throw "TierSpec total requests $total does not match TotalRequests $ExpectedRequests"
    }
    return @($tiers.ToArray())
}

function New-UserAssignments {
    param(
        [object[]]$Tiers,
        [long]$StartUserId,
        [int]$Seed
    )

    $assignments = New-Object System.Collections.Generic.List[long]
    $distribution = New-Object System.Collections.Generic.List[object]
    $nextUserId = $StartUserId

    foreach ($tier in $Tiers) {
        for ($u = 0; $u -lt $tier.user_count; $u++) {
            $userId = $nextUserId
            $nextUserId++
            for ($i = 0; $i -lt $tier.requests_each; $i++) {
                $assignments.Add($userId)
            }
            $distribution.Add([pscustomobject]@{
                user_id = $userId
                requests = $tier.requests_each
            })
        }
    }

    $rng = [System.Random]::new($Seed)
    for ($i = $assignments.Count - 1; $i -gt 0; $i--) {
        $j = $rng.Next(0, $i + 1)
        $tmp = $assignments[$i]
        $assignments[$i] = $assignments[$j]
        $assignments[$j] = $tmp
    }

    $topUsers = $distribution |
        Sort-Object -Property requests, user_id -Descending |
        Select-Object -First 20

    return [pscustomobject]@{
        assignments = [long[]]@($assignments.ToArray())
        top_users = @($topUsers)
        distinct_users = $distribution.Count
    }
}

function New-SkewedVegetaTargets {
    param(
        [string]$Path,
        [string]$BaseUrl,
        [long]$CouponId,
        [long[]]$UserAssignments,
        [string]$IdemPrefix
    )

    $targetUrl = $BaseUrl.TrimEnd("/") + "/api/v1/coupons/$CouponId/claim"
    $writer = New-Object System.IO.StreamWriter($Path, $false, (New-Object System.Text.UTF8Encoding($false)))
    for ($idx = 0; $idx -lt $UserAssignments.Length; $idx++) {
        $userId = $UserAssignments[$idx]
        $target = [ordered]@{
            method = "POST"
            url = $targetUrl
            header = [ordered]@{
                "X-User-ID" = @("$userId")
                "Idempotency-Key" = @("${IdemPrefix}_${idx}")
            }
        } | ConvertTo-Json -Compress -Depth 4
        $writer.WriteLine($target)
    }
    $writer.Dispose()
}

function Read-StatusSummary {
    param([string]$Path)

    $statusCounts = [ordered]@{}
    $transportErrors = 0
    $transportErrorCounts = @{}
    $total = 0

    foreach ($line in Get-Content $Path) {
        if ([string]::IsNullOrWhiteSpace($line)) {
            continue
        }
        $total++
        $item = $line | ConvertFrom-Json
        $code = [string]$item.code
        if (-not [string]::IsNullOrWhiteSpace($code)) {
            if (-not $statusCounts.Contains($code)) {
                $statusCounts[$code] = 0
            }
            $statusCounts[$code]++
        }
        if ($item.error -and ($code -eq "" -or $code -eq "0")) {
            $transportErrors++
            $err = [string]$item.error
            if (-not $transportErrorCounts.ContainsKey($err)) {
                $transportErrorCounts[$err] = 0
            }
            $transportErrorCounts[$err]++
        }
    }

    return [pscustomobject]@{
        status_counts = $statusCounts
        total = $total
        transport_errors = $transportErrors
        transport_error_top = @(
            $transportErrorCounts.GetEnumerator() |
                Sort-Object -Property Value -Descending |
                Select-Object -First 10 |
                ForEach-Object {
                    [ordered]@{
                        error = $_.Key
                        count = $_.Value
                    }
                }
        )
    }
}

$repoRoot = Split-Path $PSScriptRoot -Parent
$stamp = New-RunStamp
if (-not $OutputDir) {
    $OutputDir = "codex/tmp/skewed_conflict_${stamp}"
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

if ($Stock -le 0) {
    throw "Stock must be > 0"
}
if ($PerUserLimit -le 0) {
    throw "PerUserLimit must be > 0"
}
if ($TotalRequests -le 0) {
    throw "TotalRequests must be > 0"
}
if ($Rate -le 0) {
    throw "Rate must be > 0"
}
if ($DurationSeconds -le 0) {
    throw "DurationSeconds must be > 0"
}
if ($Workers -le 0 -or $MaxWorkers -le 0 -or $Connections -le 0) {
    throw "Workers, MaxWorkers and Connections must be > 0"
}

$tiers = Parse-TierSpec -Spec $TierSpec -ExpectedRequests $TotalRequests
$assignmentInfo = New-UserAssignments -Tiers $tiers -StartUserId $StartUserId -Seed $Seed
$userAssignments = $assignmentInfo.assignments

$stopScript = Join-Path $PSScriptRoot "quan-stop.ps1"
$auditScript = Join-Path $PSScriptRoot "quan-audit-ledger.ps1"
$mountSpec = "${repoRoot}:/work"
$containerBaseUrl = $BaseUrl -replace "127\.0\.0\.1", "host.docker.internal" -replace "localhost", "host.docker.internal"
$summaryPath = Join-Path $outputDirAbs "summary.json"
$targetsAbs = Join-Path $outputDirAbs "targets.jsonl"
$resultBinAbs = Join-Path $outputDirAbs "result.bin"
$reportAbs = Join-Path $outputDirAbs "report.json"
$encodedAbs = Join-Path $outputDirAbs "results.jsonl"
$auditAbs = Join-Path $outputDirAbs "audit.json"
$stdoutLog = Join-Path $outputDirAbs "quan.out.log"
$stderrLog = Join-Path $outputDirAbs "quan.err.log"
$pidFile = Join-Path $outputDirAbs "quan.pid"

$previousConfig = $env:CONFIG_PATH
$proc = $null

& $stopScript -Port $Port -PidFile $pidFile -KillPortOwner -Quiet

try {
    $env:CONFIG_PATH = $ConfigPath
    Push-Location $repoRoot
    $proc = Start-Process -FilePath "go" -ArgumentList @("run", "./examples/Quan") -PassThru -RedirectStandardOutput $stdoutLog -RedirectStandardError $stderrLog
    Set-Content -Path $pidFile -Value $proc.Id

    Wait-QuanReady -Url ($BaseUrl.TrimEnd("/") + $PingPath) -TimeoutSeconds 20

    go run ./examples/Quan/bench/cmd/benchprep `
        -dsn $MySQLDsn `
        -redis-addr $RedisAddr `
        -coupon-id $CouponId `
        -stock $Stock `
        -per-user-limit $PerUserLimit `
        -campaign-name "skewed_conflict_${stamp}"

    New-SkewedVegetaTargets `
        -Path $targetsAbs `
        -BaseUrl $containerBaseUrl `
        -CouponId $CouponId `
        -UserAssignments $userAssignments `
        -IdemPrefix "skewed_${stamp}"

    $attackArgs = @(
        "run", "--rm",
        "-v", $mountSpec,
        "-w", "/work",
        "peterevans/vegeta",
        "vegeta", "attack",
        "-format=json",
        "-targets", (To-ContainerPath -RepoRoot $repoRoot -Path $targetsAbs),
        "-output", (To-ContainerPath -RepoRoot $repoRoot -Path $resultBinAbs),
        "-workers", "$Workers",
        "-max-workers", "$MaxWorkers",
        "-connections", "$Connections",
        "-max-connections", "$Connections",
        "-rate", "$Rate",
        "-duration", ("{0}s" -f $DurationSeconds),
        "-timeout", ("{0}s" -f $RequestTimeoutSeconds),
        "-keepalive=true",
        "-name", "quan_skewed_conflict"
    )
    & docker @attackArgs
    if ($LASTEXITCODE -ne 0) {
        throw "vegeta attack failed"
    }

    $reportJson = & docker run --rm -v $mountSpec -w /work peterevans/vegeta vegeta report -type=json (To-ContainerPath -RepoRoot $repoRoot -Path $resultBinAbs)
    if ($LASTEXITCODE -ne 0) {
        throw "vegeta report failed"
    }
    $reportJson | Set-Content -Path $reportAbs -Encoding utf8

    & docker run --rm -v $mountSpec -w /work peterevans/vegeta vegeta encode -to=json (To-ContainerPath -RepoRoot $repoRoot -Path $resultBinAbs) | Set-Content -Path $encodedAbs -Encoding utf8
    if ($LASTEXITCODE -ne 0) {
        throw "vegeta encode failed"
    }

    $report = Get-Content $reportAbs -Raw | ConvertFrom-Json
    $statusSummary = Read-StatusSummary -Path $encodedAbs
    $auditRaw = & $auditScript -CouponId $CouponId -MySQLDsn $MySQLDsn -OutputPath $auditAbs
    $audit = $auditRaw | ConvertFrom-Json

    $successResponses = 0
    $conflictResponses = 0
    $serverErrors = 0
    foreach ($key in $statusSummary.status_counts.Keys) {
        if ($key -match "^2\d\d$") {
            $successResponses += [int]$statusSummary.status_counts[$key]
        }
        elseif ($key -eq "409") {
            $conflictResponses += [int]$statusSummary.status_counts[$key]
        }
        elseif ($key -match "^5\d\d$") {
            $serverErrors += [int]$statusSummary.status_counts[$key]
        }
    }

    $summary = [ordered]@{
        scenario = "skewed_high_conflict"
        checked_at = (Get-Date).ToUniversalTime().ToString("o")
        config = [ordered]@{
            coupon_id = $CouponId
            stock = $Stock
            per_user_limit = $PerUserLimit
            total_requests = $TotalRequests
            rate_per_second = $Rate
            duration_seconds = $DurationSeconds
            request_timeout_seconds = $RequestTimeoutSeconds
            workers = $Workers
            max_workers = $MaxWorkers
            connections = $Connections
            tier_spec = $TierSpec
            distinct_users = $assignmentInfo.distinct_users
            top_users = @($assignmentInfo.top_users)
        }
        results = [ordered]@{
            requests_sent = $report.requests
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
            success_responses = $successResponses
            conflict_responses = $conflictResponses
            server_errors = $serverErrors
            transport_errors = $statusSummary.transport_errors
            transport_error_top = $statusSummary.transport_error_top
        }
        ledger_audit = $audit
        derived = [ordered]@{
            success_delta_vs_claims = ($successResponses - $audit.claims.persisted_claims)
            oversell_claims = $audit.claims.oversell_claims
            overflow_claims = $audit.claims.overflow_claims
            available_stock_delta = $audit.claims.available_stock_delta
        }
        files = [ordered]@{
            report = $reportAbs
            encoded = $encodedAbs
            audit = $auditAbs
            stdout_log = $stdoutLog
            stderr_log = $stderrLog
        }
    }

    $summaryJson = $summary | ConvertTo-Json -Depth 8
    $summaryJson | Set-Content -Path $summaryPath -Encoding utf8
    $summaryJson
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
