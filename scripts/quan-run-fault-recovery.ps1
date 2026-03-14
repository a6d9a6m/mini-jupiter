param(
    [string]$OutputPath = "",
    [int]$RepeatCount = 1,
    [switch]$FailFast,
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
if (-not $OutputPath) {
    $OutputPath = "codex/tmp/fault_recovery_${stamp}.json"
}
if ($RepeatCount -le 0) {
    throw "RepeatCount must be > 0"
}

$scenarios = @(
    @{ name = "relay_publish_failure"; package = "./examples/Quan/internal/task"; test = "^TestE2E_FaultInjection_RelayPublishFailure_RetryThenRecover$"; purpose = "commit success then relay publish failure" },
    @{ name = "relay_mark_published_failure"; package = "./examples/Quan/internal/task"; test = "^TestE2E_FaultInjection_RelayMarkPublishedFailure_RecoveredByDispatchScan$"; purpose = "publish success but mark-published failure" },
    @{ name = "consumer_mark_success_failure"; package = "./examples/Quan/internal/task"; test = "^TestE2E_FaultInjection_ConsumerMarkSuccessFailure_RecoveredByCompensation$"; purpose = "consumer handler success then mark-success failure" },
    @{ name = "retry_schedule_failure"; package = "./examples/Quan/internal/task"; test = "^TestE2E_TaskPipeline_RetryScheduleFailure_RecoveredByCompensation$"; purpose = "retry scheduling failure recovered by compensation" },
    @{ name = "duplicate_ready_delivery"; package = "./examples/Quan/internal/task"; test = "^TestE2E_FaultInjection_DuplicateReadyDelivery_ConsumesOnce$"; purpose = "duplicate delivery does not multiply side effects" },
    @{ name = "short_redis_outage"; package = "./examples/Quan/internal/task"; test = "^TestE2E_FaultInjection_ShortRedisOutageOnPublishReady_Recovered$"; purpose = "transient Redis publish outage" },
    @{ name = "stale_running_recovery"; package = "./examples/Quan/internal/task"; test = "^TestE2E_FaultInjection_StaleRunningRecoveredAfterRestartLikePause$"; purpose = "stale running task recovered after pause" },
    @{ name = "dlq_replay"; package = "./examples/Quan/internal/task"; test = "^TestE2E_TaskPipeline_DLQReplay_ManualRecover$"; purpose = "manual DLQ replay to final success" }
)

$results = @()
$overallPass = $true

foreach ($scenario in $scenarios) {
    for ($iteration = 1; $iteration -le $RepeatCount; $iteration++) {
        $startedAt = [DateTime]::UtcNow
        $scenarioPass = $true
        $failureMessage = ""
        $duration = Measure-Command {
            try {
                & go test $scenario.package -run $scenario.test -count=1 -v | Out-Host
                $exitCode = $LASTEXITCODE
                if ($exitCode -ne 0) {
                    throw "scenario $($scenario.name) iteration $iteration failed with exit code $exitCode"
                }
            } catch {
                $scenarioPass = $false
                $failureMessage = $_.Exception.Message
            }
        }
        $results += [pscustomobject]@{
            name = $scenario.name
            iteration = $iteration
            package = $scenario.package
            test = $scenario.test
            purpose = $scenario.purpose
            started_at = $startedAt.ToString("o")
            duration_seconds = [Math]::Round($duration.TotalSeconds, 3)
            pass = $scenarioPass
            failure = $failureMessage
        }
        if (-not $scenarioPass) {
            $overallPass = $false
            if ($FailFast) {
                break
            }
        }
    }
    if (-not $overallPass -and $FailFast) {
        break
    }
}

$scenarioSummaries = @()
foreach ($scenario in $scenarios) {
    $scenarioRuns = @($results | Where-Object { $_.name -eq $scenario.name })
    if ($scenarioRuns.Count -eq 0) {
        continue
    }
    $passCount = @($scenarioRuns | Where-Object { $_.pass }).Count
    $avgDuration = (($scenarioRuns | Measure-Object -Property duration_seconds -Average).Average)
    $scenarioSummaries += [pscustomobject]@{
        name = $scenario.name
        purpose = $scenario.purpose
        runs = $scenarioRuns.Count
        pass_count = $passCount
        pass_rate = [Math]::Round(($passCount / $scenarioRuns.Count), 4)
        avg_duration_seconds = [Math]::Round([double]$avgDuration, 3)
    }
}

$totalPassCount = @($results | Where-Object { $_.pass }).Count
$avgRecovery = (($results | Measure-Object -Property duration_seconds -Average).Average)

$summary = [pscustomobject]@{
    checked_at = [DateTime]::UtcNow.ToString("o")
    scenario_count = $scenarios.Count
    repeat_count = $RepeatCount
    total_event_samples = $results.Count
    pass_count = $totalPassCount
    pass_rate = [Math]::Round(($totalPassCount / [Math]::Max($results.Count, 1)), 4)
    avg_recovery_seconds = [Math]::Round([double]$avgRecovery, 3)
    pass = $overallPass
    scenario_summaries = $scenarioSummaries
    scenarios = $results
}

$dir = Split-Path -Parent $OutputPath
if ($dir -and -not (Test-Path $dir)) {
    New-Item -ItemType Directory -Path $dir -Force | Out-Null
}

$json = $summary | ConvertTo-Json -Depth 6
$json | Set-Content -Path $OutputPath -Encoding utf8
$json

if (-not $overallPass) {
    exit 2
}
