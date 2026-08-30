# Secrets & Identity

## Rules
- Never persist long-lived cloud credentials on generic workers.
- Prefer OIDC/workload identity where providers support it.
- Mint credentials for a specific Work/Node/purpose.
- Expire automatically.
- Deny fork-originated privileged access by default.
- Record secret access metadata without recording secret values.

## Worker identity
Worker enrollment creates a cryptographic identity bound to organization/pool/trust class. Revocation must take effect without reinstalling the fleet.
