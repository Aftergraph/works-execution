package worker

// commandShell returns the host-native command interpreter used by the
// WORKS subprocess worker.
//
// The caller provides goos explicitly so the mapping is deterministic and
// unit-testable on Linux while still producing a native Windows invocation.
//
// WORKS owns durable execution state and leases. This helper changes only the
// local process adapter selected after a lease has already been granted.
func commandShell(goos, command string) (string, []string) {
	switch goos {
	case "windows":
		// Windows Server/desktop images consistently ship Windows PowerShell.
		// -NoProfile prevents operator profile scripts from silently changing
		// execution semantics; -NonInteractive avoids prompts that would hang a
		// leased work item.
		return "powershell.exe", []string{
			"-NoLogo",
			"-NoProfile",
			"-NonInteractive",
			"-Command",
			command + `; $ag_ok=$?; $ag_ec=$LASTEXITCODE; if (-not $ag_ok) { if ($null -ne $ag_ec -and $ag_ec -ne 0) { exit $ag_ec }; exit 1 }; if ($null -ne $ag_ec) { exit $ag_ec }; exit 0`,
		}
	default:
		return "sh", []string{"-c", command}
	}
}
