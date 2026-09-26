# Faultbench results

- **Date (UTC):** 2026-09-26T14:56:29Z
- **Commit:** 4bb21e799dfa01d258c27015214dadbca8595396
- **Hardware:** amd64, 12 cores, ~0 MiB RAM, windows/amd64
- **Command:** `C:\Users\BIT\Documents\github-new\hookrelay\bin\faultbench.exe -n 500 -restarts 1 -out bench\RESULTS_SMALL.md -database-url postgres://hookrelay:hookrelay@127.0.0.1:55432/hookrelay?sslmode=disable -bin-dir bin`

## Run parameters

| Param | Value |
| --- | --- |
| Events | 500 |
| Chaos mix | 30% failure (10% 5xx + 5% timeout + 5% slow + 10% reset) |
| Restarts requested | 1 |
| Restarts performed | 1 |
| Insert wall time | 6.4863465s |
| Total wall time | 51.6074498s |

## Measured

| Metric | Value |
| --- | --- |
| Delivered % (unique / N) | 100.0000% (500 / 500) |
| Lost events (neither delivered nor DLQ) | 0 |
| Duplicate deliveries | 4 |
| DLQ count | 0 |
| End-to-end delay p50 (s) | 5.8479 |
| End-to-end delay p99 (s) | 15.7060 |
| Chaos accepted | 504 |
| Chaos bad signatures | 0 |

