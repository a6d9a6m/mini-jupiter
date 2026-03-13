param(
    [int]$Port = 8081,
    [string]$PidFile = "codex/tmp/quan-bench.pid",
    [switch]$KillPortOwner,
    [switch]$Quiet
)

$ErrorActionPreference = "Stop"

function Write-Info {
    param([string]$Message)
    if (-not $Quiet) {
        Write-Host $Message
    }
}

function Stop-ByPidFile {
    if (-not (Test-Path $PidFile)) {
        return
    }

    $stored = (Get-Content $PidFile -ErrorAction SilentlyContinue | Select-Object -First 1).Trim()
    if (-not $stored) {
        Remove-Item $PidFile -ErrorAction SilentlyContinue
        return
    }

    $proc = Get-Process -Id $stored -ErrorAction SilentlyContinue
    if ($proc) {
        Write-Info "Stopping Quan process from pid file: $stored"
        Stop-Process -Id $stored -Force
        Start-Sleep -Milliseconds 300
    }
    Remove-Item $PidFile -ErrorAction SilentlyContinue
}

function Stop-ByPort {
    $listeners = @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue | Select-Object -ExpandProperty OwningProcess -Unique)
    foreach ($owner in $listeners) {
        if (-not $owner) {
            continue
        }
        if (-not $KillPortOwner) {
            throw "Port $Port is still occupied by PID $owner. Re-run with -KillPortOwner to stop it."
        }
        $proc = Get-Process -Id $owner -ErrorAction SilentlyContinue
        if ($proc) {
            Write-Info "Stopping port owner on :$Port => PID $owner ($($proc.ProcessName))"
            Stop-Process -Id $owner -Force
            Start-Sleep -Milliseconds 300
        }
    }
}

Stop-ByPidFile
Stop-ByPort
