param(
    [string]$ConfigPath = "examples/http-server/config.baseline.yaml",
    [string]$Url = "http://localhost:8080/ping",
    [int]$DurationSeconds = 45,
    [int]$TotalRequests = 10000000,
    [int]$Concurrency = 50,
    [int]$Runs = 3
)

$ErrorActionPreference = "Stop"

function Ensure-Hey {
    $hey = Get-Command hey -ErrorAction SilentlyContinue
    if (-not $hey) {
        Write-Host "hey not found. Install with: go install github.com/rakyll/hey@latest"
        exit 1
    }
}

function Start-Server {
    $env:CONFIG_PATH = $ConfigPath
    $proc = Start-Process -FilePath "go" -ArgumentList @("run", "./examples/http-server") -PassThru -WindowStyle Hidden
    Start-Sleep -Seconds 2
    return $proc
}

function Run-Hey {
    $OutputEncoding = [System.Text.UTF8Encoding]::new($false)
    [Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
    $args = @("-n", $TotalRequests, "-c", $Concurrency, $Url)
    $results = @()
    for ($i = 1; $i -le $Runs; $i++) {
        $timestamp = Get-Date -Format "yyyyMMdd_HHmmss"
        $outFile = "bench/results/baseline_${timestamp}_run${i}.txt"
        $result = hey @args | Out-String
        $result | Set-Content -Path $outFile -Encoding utf8
        $results += $result
        Write-Host "Run $i saved to $outFile"
        Start-Sleep -Seconds 2
    }
    return $results
}

Ensure-Hey
$proc = Start-Server
try {
    Run-Hey | ForEach-Object { Write-Output $_ }
}
finally {
    if ($proc -and !$proc.HasExited) {
        Stop-Process -Id $proc.Id -Force
    }
}
