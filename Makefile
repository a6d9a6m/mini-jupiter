.PHONY: bench quan-bench quan-stop quan-high-conflict quan-audit quan-fault-recovery quan-capacity

bench:
	powershell -ExecutionPolicy Bypass -File .\bench\run.ps1

quan-bench:
	powershell -ExecutionPolicy Bypass -File .\scripts\quan-run-bench.ps1

quan-stop:
	powershell -ExecutionPolicy Bypass -File .\scripts\quan-stop.ps1 -KillPortOwner

quan-high-conflict:
	powershell -ExecutionPolicy Bypass -File .\scripts\quan-run-high-conflict.ps1

quan-audit:
	powershell -ExecutionPolicy Bypass -File .\scripts\quan-audit-ledger.ps1

quan-fault-recovery:
	powershell -ExecutionPolicy Bypass -File .\scripts\quan-run-fault-recovery.ps1

quan-capacity:
	powershell -ExecutionPolicy Bypass -File .\scripts\quan-run-capacity.ps1
