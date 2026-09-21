# Windows WORKS Worker — native Lenovo execution

This runbook provisions a Windows machine as an **Aftergraph WORKS BYOC runner**.
It removes GitHub Actions and Desktop Commander from the execution path.

## Architecture

```text
Runtime
  -> Trust Gateway / AIE admission
  -> WORKS durable Work + lease
  -> outbound Windows works-worker
  -> PowerShell subprocess on the leased node
  -> artifact + evidence
  -> WORKS terminal state
  -> independent verification
```

The worker polls the WORKS control plane outbound. No inbound shell, RDP,
MCP Desktop Commander port, or public Windows listener is required.

## Contract

- stable worker id, e.g. `wrkr_jonas_lenovo`;
- dedicated pool, e.g. `jonas-lenovo`;
- OS/arch advertised from Go runtime as `windows/amd64`;
- pool-scoped Work cannot be leased by a runner outside that pool;
- enrollment secret mints short-lived worker JWTs;
- private source checkout uses a separately supplied GitHub token when required;
- secrets are entered interactively and stored as current-user DPAPI ciphertext.

The Windows host adapter uses:

```text
powershell.exe -NoLogo -NoProfile -NonInteractive -Command <Node.Run>
```

instead of the previous POSIX-only `sh -c` path.

## Build on trusted Aftergraph compute

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o dist/works-worker-windows-amd64.exe ./cmd/works-worker
```

Record source SHA and executable SHA-256 before transfer.

## Install on Jonas Lenovo

```powershell
.\ops\windows\install-works-worker.ps1 -WorkerExe C:\Aftergraph\staging\works-worker-windows-amd64.exe -ApiUrl https://<WORKS-CONTROL-PLANE> -WorkerId wrkr_jonas_lenovo -Pool jonas-lenovo
```

The installer rejects plaintext non-loopback HTTP, prompts for secrets without
placing them in shell history, DPAPI-protects them, restricts the secret file
ACL, installs a restartable logon task, and starts it immediately.

## Proof gate

Do not call Lenovo native execution active until:

1. `works runners --pool jonas-lenovo --alive` shows the exact runner.
2. Runner capabilities include `windows` and `amd64`.
3. A no-op Work constrained to `pool: jonas-lenovo` reaches `SUCCEEDED`.
4. A Work constrained to another pool records zero attempts on Lenovo.
5. Killing the worker causes lease expiry/recovery as designed.
6. Evidence signer equals the Lenovo worker id and is bound to the exact Work.
7. Consequential work is admitted through Runtime -> AIE -> Trust Gateway.

## Boundary

This is a durable execution adapter, not a general remote shell. Interactive
desktop/UI work remains a Computer Node provider concern. Non-interactive host
commands belong in governed WORKS Work instead of a Desktop Commander clone.
