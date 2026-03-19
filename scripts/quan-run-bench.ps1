param(
    [string]$ConfigPath = "examples/Quan/config.sample.yaml",
    [string]$BaseUrl = "http://127.0.0.1:8081",
    [string]$RedisAddr = "127.0.0.1:6379",
    [int]$Port = 8081,
    [string]$PingPath = "/ping",
    [string]$MySQLDsn = "root:root@tcp(127.0.0.1:3306)/mini_jupiter?parseTime=true&loc=Local&charset=utf8mb4",
    [string]$Scenario = "baseline",
    [long]$CouponId = 9501,
    [int]$Stock = 1000,
    [int]$PerUserLimit = 1,
    [string]$CampaignName = "",
    [int]$Requests = 4000,
    [int]$Concurrency = 40,
    [string]$UserMode = "unique",
    [int]$UserPool = 4000,
    [long]$StartUserId = 100000,
    [string]$IdemMode = "unique",
    [string]$IdemPrefix = "bench",
    [string]$FixedIdemKey = "",
    [string]$ReportOut = "",
    [string]$PidFile = "codex/tmp/quan-bench.pid",
    [int]$ReadyTimeoutSeconds = 15
)

$ErrorActionPreference = "Stop"

function New-RunStamp {
    return Get-Date -Format "yyyyMMdd_HHmmss"
}

function Ensure-Directory {
    param([string]$Path)
    $dir = Split-Path -Parent $Path
    if ($dir -and -not (Test-Path $dir)) {
        New-Item -ItemType Directory -Path $dir -Force | Out-Null
    }
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
        } catch {
        }
        Start-Sleep -Milliseconds 500
    }
    throw "Quan server did not become ready at $Url within ${TimeoutSeconds}s"
}

if (-not $CampaignName) {
    $CampaignName = $Scenario
}

$stamp = New-RunStamp
if (-not $ReportOut) {
    $ReportOut = "examples/Quan/doc/report/${Scenario}_${stamp}.json"
}

$stdoutLog = "codex/tmp/quan-bench-${stamp}.out.log"
$stderrLog = "codex/tmp/quan-bench-${stamp}.err.log"
Ensure-Directory $stdoutLog
Ensure-Directory $stderrLog
Ensure-Directory $ReportOut
Ensure-Directory $PidFile

$stopScript = Join-Path $PSScriptRoot "quan-stop.ps1"
& $stopScript -Port $Port -PidFile $PidFile -KillPortOwner -Quiet

$previousConfig = $env:CONFIG_PATH
$env:CONFIG_PATH = $ConfigPath
$proc = $null

try {
    $proc = Start-Process -FilePath "go" -ArgumentList @("run", "./examples/Quan") -PassThru -RedirectStandardOutput $stdoutLog -RedirectStandardError $stderrLog
    Set-Content -Path $PidFile -Value $proc.Id

    Wait-QuanReady -Url ($BaseUrl.TrimEnd("/") + $PingPath) -TimeoutSeconds $ReadyTimeoutSeconds

    go run ./examples/Quan/bench/cmd/benchprep `
        -dsn $MySQLDsn `
        -redis-addr $RedisAddr `
        -coupon-id $CouponId `
        -stock $Stock `
        -per-user-limit $PerUserLimit `
        -campaign-name $CampaignName

    $benchArgs = @(
        "run", "./examples/Quan/bench/cmd/benchclaim",
        "-scenario", $Scenario,
        "-base-url", $BaseUrl,
        "-coupon-id", $CouponId,
        "-requests", $Requests,
        "-concurrency", $Concurrency,
        "-user-mode", $UserMode,
        "-user-pool", $UserPool,
        "-start-user-id", $StartUserId,
        "-idem-mode", $IdemMode,
        "-idem-prefix", $IdemPrefix,
        "-report-out", $ReportOut
    )
    if ($FixedIdemKey) {
        $benchArgs += @("-fixed-idem-key", $FixedIdemKey)
    }

    go @benchArgs

    Write-Host "Quan benchmark completed."
    Write-Host "Scenario : $Scenario"
    Write-Host "Report   : $ReportOut"
    Write-Host "Stdout   : $stdoutLog"
    Write-Host "Stderr   : $stderrLog"
    Write-Host "PID      : $($proc.Id)"
}
finally {
    if ($null -ne $previousConfig) {
        $env:CONFIG_PATH = $previousConfig
    } else {
        Remove-Item Env:CONFIG_PATH -ErrorAction SilentlyContinue
    }

    & $stopScript -Port $Port -PidFile $PidFile -KillPortOwner -Quiet
}
