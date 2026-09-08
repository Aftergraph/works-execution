# Security Policy

This is a Hard-class repository (policy, credentials, verdicts, durable state, or the security boundary). Changes that weaken fail-closed behavior, trust boundaries, or credential handling require explicit owner review and test evidence.

## Reporting

Do not disclose suspected vulnerabilities in public issues, pull requests, or commit messages. Report privately to the maintainer (@JonasAbde) through an established private channel with: description, affected file/commit, reproduction, impact, and required privileges. Never include live credentials or tokens in a report; use redacted examples.

## Secrets

No production credentials, tokens, or signing material belong in this repository. Test fixtures must be ephemeral and clearly non-secret.

## Supported versions

Until the first tagged release, only the current `main` branch receives security fixes.
