# Faultbench results

- **Date (UTC):** 2026-09-26T15:05:17Z
- **Commit:** d0f44fcb31808a47df5d0921c5e65344512cf41c
- **Hardware:** AMD Ryzen 5 7530U with Radeon Graphics, 12 logical CPUs, 23873 MiB RAM, windows/amd64
- **Command:** `C:\Users\BIT\Documents\github-new\hookrelay\bin\faultbench.exe -n 10000 -restarts 5 -out bench\RESULTS.md -database-url postgres://hookrelay:hookrelay@127.0.0.1:55432/hookrelay?sslmode=disable -bin-dir bin`

## Run parameters

| Param | Value |
| --- | --- |
| Events | 10000 |
| Chaos mix | 30% failure (10% 5xx + 5% timeout + 5% slow + 10% reset) |
| Restarts requested | 5 |
| Restarts performed | 5 |
| Insert wall time | 33.9986444s |
| Total wall time | 7m54.560468s |

## Measured

| Metric | Value |
| --- | --- |
| Delivered % (unique / N) | 100.0000% (10000 / 10000) |
| Lost events (neither delivered nor DLQ) | 0 |
| Duplicate deliveries | 0 |
| DLQ count | 0 |
| End-to-end delay p50 (s) | 263.9354 |
| End-to-end delay p99 (s) | 455.1377 |
| Chaos accepted | 10000 |
| Chaos bad signatures | 0 |

## Notes

- Hardware line was filled from `Get-CimInstance Win32_Processor` / `Win32_ComputerSystem` on the same machine immediately after the run (the binary's self-reported RAM field printed `0` due to a broken `wmic` fallback; CPU core count and OS matched).
- Postgres was the embedded instance started earlier via `cmd/devpg` on port `55432` (Docker was not available on this host).
- High p50/p99 reflect backlog depth under the 30% chaos mix plus five process kills during drain, not per-attempt RTT.
