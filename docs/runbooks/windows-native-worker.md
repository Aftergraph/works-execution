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
- pool-scoped Work can be leased only when the registered runner is active,
  has a heartbeat no older than three 10-second heartbeat intervals, and has
  the exact `pool:<name>` label; a missing registry or heartbeat fails closed;
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

DPAPI and the file ACL protect the secret file from other Windows identities
and offline copying; they do not isolate it from code running as the same user.
The scheduled task runs under the installing user, and leased PowerShell
commands run under that same identity. They inherit its normal filesystem and
network permissions. The production host-command path does not pass a sandbox
manifest or enforce OS-level filesystem/network isolation. Treat submitted
Work as trusted code until a restricted execution identity or VM boundary is
implemented and verified.

## Proof gate

Do not call Lenovo native execution active until:

1. `works runners --pool jonas-lenovo --alive` shows the exact runner.
2. Runner capabilities include `windows` and `amd64`.
3. The `alive` result requires an active registration and a fresh worker
   heartbeat; an old registration alone is not availability proof.
4. A no-op Work constrained to `pool: jonas-lenovo` reaches `SUCCEEDED`.
5. A Work constrained to another pool records zero attempts on Lenovo.
6. Killing the worker causes lease expiry/recovery as designed.
7. The worker-reported evidence `signer` field matches the Lenovo worker id
   and is bound to the exact Work. This field is not cryptographic machine
   attestation.
8. Consequential work is admitted through Runtime -> AIE -> Trust Gateway.

## Boundary

Each lease-authorized Work runs its `Node.Run` as a native host command under
the scheduled-task identity. Interactive desktop/UI work remains a Computer
Node provider concern. The native command path is not a filesystem or network
sandbox, so only trusted Work should reach it until isolation is implemented.

The GitHub Actions runner is a separate diagnostic/probe path. Passing its
probe proves GitHub matched the requested labels and the job ran on a machine
whose self-reported `COMPUTERNAME` is `JONAS-LENOVO`, then published its receipt.
The hostname is not cryptographic machine identity; the probe does not prove
WORKS enrollment or lease execution.
This worker contract is outbound HTTPS with a short-lived worker JWT. Do not
describe it as outbound mTLS or cryptographic machine attestation. Those
require separate certificate and attestation evidence. Hyper-V or other
privileged operations also require an explicitly delegated Windows identity
and a privilege check from the worker process; an interactive elevated shell
is not proof of the scheduled worker's token.
