#!/usr/bin/env python3
"""Contract Freeze Slice 0 — schema materializer + manifest generator.

Emits the 20 frozen contracts (ADR-0008..0027 + final-freeze-review) as
draft-07 JSON Schemas and a machine-readable Contract Freeze Manifest with
per-file sha256 digests. Deterministic: re-running rewrites identical bytes
unless a schema genuinely changed (then hashes change = drift, by design).
"""
import hashlib
import json
import os
import sys

OUT = os.path.join(os.path.dirname(__file__), "schemas")

SCHEMAS = {
    "work.schema": {
        "version": "1.0", "adr": "ADR-0008, ADR-0009",
        "owner": "works-kernel",
        "schema": {
            "$id": "contract:work.schema/1.0",
            "type": "object",
            "required": ["id", "created_at", "updated_at", "source", "objective", "graph", "requirements", "policy", "state"],
            "properties": {
                "id": {"type": "string", "pattern": "^[a-z0-9-]+:[a-f0-9]{32}$"},
                "created_at": {"type": "string"},
                "updated_at": {"type": "string"},
                "source": {"type": "object"},
                "objective": {"type": "object"},
                "graph": {"type": "object", "required": ["nodes"], "properties": {"nodes": {"type": "object"}}},
                "requirements": {"type": "object"},
                "policy": {"type": "object"},
                "state": {"enum": ["CREATED", "PLANNING", "QUEUED", "RUNNING", "VERIFYING", "SUCCEEDED", "BLOCKED", "FAILED", "CANCELLED", "WAITING_HUMAN", "SUSPENDED", "BUDGET_EXHAUSTED"]},
                "attempts": {"type": "array"},
                "artifacts": {"type": "array"},
                "evidence": {"type": "array"},
                "idempotency_key": {"type": "string"},
                "correlation_id": {"type": "string"},
                "budget_ceiling": {
                    "type": "object",
                    "required": ["compute_eur", "wall_clock_h"],
                    "properties": {"compute_eur": {"type": "number", "minimum": 0}, "wall_clock_h": {"type": "number", "minimum": 0}}
                },
                "verification": {
                    "type": "array",
                    "items": {"type": "object", "required": ["criterion"], "properties": {"criterion": {"type": "string"}, "kind": {"enum": ["deterministic", "human_review"]}}}
                },
                "purpose_bindings": {"type": "array", "items": {"type": "string"}},
                "kill_switch": {"enum": ["always", "policy"]},
                "handoff": {"type": "object", "required": ["state_snapshot", "narrative", "decision_log", "priority_queue", "warnings", "payload_schema"], "properties": {"state_snapshot": {"type": "object"}, "narrative": {"type": "string"}, "decision_log": {"type": "array", "items": {"type": "string"}}, "priority_queue": {"type": "array", "items": {"type": "string"}}, "warnings": {"type": "array", "items": {"type": "string"}}, "payload_schema": {"type": "string"}}}
            }
        }
    },
    "kernel.budget": {
        "version": "1.0", "adr": "ADR-0009", "owner": "works-kernel",
        "schema": {
            "$id": "contract:kernel.budget/1.0",
            "type": "object",
            "required": ["work_id", "ceiling", "reserved", "consumed", "clock_state"],
            "properties": {
                "work_id": {"type": "string"},
                "ceiling": {"type": "object", "required": ["compute_eur", "wall_clock_h"]},
                "reserved": {"type": "number", "minimum": 0},
                "consumed": {"type": "number", "minimum": 0},
                "late_bill_entries": {"type": "array", "items": {"type": "object", "required": ["amount_eur", "reason"], "properties": {"amount_eur": {"type": "number"}, "reason": {"type": "string"}}}},
                "clock_state": {"enum": ["RUNNING", "PAUSED_WAITING_HUMAN", "STOPPED"]},
                "hard_stop": {"enum": ["wall_clock", "compute", "none"]}
            },
            "allOf": [{"if": {"properties": {"clock_state": {"const": "PAUSED_WAITING_HUMAN"}}}, "then": {"properties": {"clock_running": {"const": False}}}}]
        }
    },
    "handoff.schema": {
        "version": "1.0", "adr": "ADR-0010", "owner": "works-kernel",
        "schema": {
            "$id": "contract:handoff.schema/1.0",
            "type": "object",
            "required": ["state_snapshot", "narrative", "decision_log", "priority_queue", "warnings", "payload_schema"],
            "properties": {
                "state_snapshot": {"type": "object"},
                "narrative": {"type": "string"},
                "decision_log": {"type": "array", "items": {"type": "string"}},
                "priority_queue": {"type": "array", "items": {"type": "string"}},
                "warnings": {"type": "array", "items": {"type": "string"}},
                "payload_schema": {"type": "string"}
            }
        }
    },
    "evidence.schema": {
        "version": "1.1", "adr": "ADR-0011, ADR-0024", "owner": "works-evidence",
        "schema": {
            "$id": "contract:evidence.schema/1.1",
            "type": "object",
            "required": ["bundle_id", "identity_chain", "created_at", "records"],
            "properties": {
                "bundle_id": {"type": "string"},
                "identity_chain": {"type": "object"},
                "created_at": {"type": "string"},
                "cpi_generation": {"type": "string"},
                "provider_id": {"type": "string"},
                "driver_segments": {"type": "array", "items": {"type": "object", "required": ["driver", "from_seq", "to_seq"], "properties": {"driver": {"enum": ["agent", "human"]}, "from_seq": {"type": "integer"}, "to_seq": {"type": "integer"}}}},
                "records": {"type": "array"},
                "cites_events": {"type": "array", "items": {"type": "string"}}
            }
        }
    },
    "quittance.rules": {
        "version": "1.0", "adr": "ADR-0011", "owner": "works-evidence",
        "schema": {
            "$id": "contract:quittance.rules/1.0",
            "type": "object",
            "required": ["quittance_id", "work_id", "bundle_id", "verification", "usage"],
            "properties": {
                "quittance_id": {"type": "string"},
                "work_id": {"type": "string"},
                "bundle_id": {"type": "string"},
                "verification": {"enum": ["passed", "failed"]},
                "price_hint": {"type": "number"},
                "usage": {"type": "object", "required": ["compute_eur", "wall_clock_s"], "properties": {"compute_eur": {"type": "number"}, "wall_clock_s": {"type": "integer"}, "tokens": {"type": "integer"}}},
                "idempotency": {"type": "string", "pattern": "^[a-f0-9]{64}$"}
            },
            "not": {"properties": {"verification": {"const": "failed"}, "price_hint": {"type": "number"}}}
        }
    },
    "cpi": {
        "version": "1.0", "adr": "ADR-0012, ADR-0018", "owner": "works-hal",
        "schema": {
            "$id": "contract:cpi/1.0",
            "type": "object",
            "required": ["abi", "caps"],
            "properties": {
                "abi": {"const": "cpi/1.0"},
                "caps": {"type": "array", "items": {"enum": ["fs", "shell", "git", "browser", "snap", "teardown_retain"]}, "uniqueItems": True},
                "provider_id": {"type": "string"},
                "generation": {"type": "string"}
            }
        }
    },
    "rab": {
        "version": "1.0", "adr": "ADR-0012, ADR-0014", "owner": "works-runtime",
        "schema": {
            "$id": "contract:rab/1.0",
            "type": "object",
            "required": ["abi", "caps"],
            "properties": {
                "abi": {"const": "rab/1.0"},
                "caps": {"type": "array", "items": {"enum": ["screenshot", "input", "record", "observe", "control"]}, "uniqueItems": True},
                "control_token_required": {"const": True}
            }
        }
    },
    "identity": {
        "version": "1.0", "adr": "ADR-0016", "owner": "works-identity",
        "schema": {
            "$id": "contract:identity/1.0",
            "type": "object",
            "required": ["human", "org", "device", "worker", "runtime"],
            "properties": {
                "human": {"type": "string"},
                "org": {"type": "string", "pattern": "^org_[a-f0-9]{8,}$"},
                "device": {"type": "string"},
                "worker": {"type": "object", "required": ["role"], "properties": {"role": {"enum": ["engineering", "growth", "ops", "research", "ci"]}}},
                "runtime": {"type": "object", "required": ["work_id", "lease_id"], "properties": {"work_id": {"type": "string"}, "lease_id": {"type": "string"}}},
                "service_principal": {"type": "boolean"},
                "privilege_note": {"enum": ["service_principals_never_approve"]}
            }
        }
    },
    "policy.token": {
        "version": "1.0", "adr": "ADR-0017", "owner": "works-kernel",
        "schema": {
            "$id": "contract:policy.token/1.0",
            "type": "object",
            "required": ["token_id", "work_id", "org", "scopes", "purpose_bindings", "budget_line", "expiry"],
            "properties": {
                "token_id": {"type": "string"},
                "work_id": {"type": "string"},
                "org": {"type": "string"},
                "scopes": {"type": "array", "items": {"type": "string"}, "uniqueItems": True},
                "purpose_bindings": {"type": "array", "items": {"type": "string"}},
                "budget_line": {"type": "object"},
                "expiry": {"type": "string"},
                "delegated_from": {"type": "string"}
            }
        }
    },
    "events": {
        "version": "1.0", "adr": "ADR-0019", "owner": "works-kernel",
        "schema": {
            "$id": "contract:events/1.0",
            "type": "object",
            "required": ["source", "seq", "type", "subject", "ts", "version"],
            "properties": {
                "source": {"enum": ["works-org", "pulse-device"]},
                "seq": {"type": "integer", "minimum": 1},
                "type": {"type": "string"},
                "subject": {"type": "string"},
                "payload_ref": {"type": "string"},
                "ts": {"type": "string", "description": "informative only; ordering is (source, seq)"},
                "version": {"type": "string"}
            }
        }
    },
    "sync.rules": {
        "version": "1.0", "adr": "ADR-0020", "owner": "pulse-link",
        "schema": {
            "$id": "contract:sync.rules/1.0",
            "type": "object",
            "required": ["entity", "owner", "sync_state"],
            "properties": {
                "entity": {"type": "string"},
                "owner": {"enum": ["pulse_local", "works_kernel"]},
                "sync_state": {"enum": ["queued", "synced", "failed_retry", "conflict_flag", "cleared_by_revoke"]},
                "revoke_precedence": {"const": True},
                "watermark": {"type": "string"}
            }
        }
    },
    "proto.charter": {
        "version": "1.0", "adr": "ADR-0021", "owner": "works-kernel",
        "schema": {
            "$id": "contract:proto.charter/1.0",
            "type": "object",
            "required": ["name", "version", "capabilities"],
            "properties": {
                "name": {"type": "string"},
                "version": {"type": "string", "pattern": "^[0-9]+\\.[0-9]+$"},
                "capabilities": {"type": "array", "items": {"type": "string"}},
                "n_minus_1_supported": {"type": "boolean"},
                "unknown_field_tolerance": {"const": True}
            }
        }
    },
    "secret.ref": {
        "version": "1.0", "adr": "ADR-0022", "owner": "works-kernel",
        "schema": {
            "$id": "contract:secret.ref/1.0",
            "type": "object",
            "required": ["ref", "scope"],
            "additionalProperties": False,
            "properties": {
                "ref": {"type": "string", "pattern": "^secret://[a-z0-9-]+/[A-Za-z0-9_-]+$"},
                "work_id": {"type": "string"},
                "scope": {"type": "string"}
            }
        }
    },
    "brain.ns": {
        "version": "1.0", "adr": "ADR-0023", "owner": "works-brain",
        "schema": {
            "$id": "contract:brain.ns/1.0",
            "type": "object",
            "required": ["path"],
            "properties": {
                "path": {"type": "string", "pattern": "^/org/[a-f0-9-]+/(missions|decisions|capabilities|evidence|notes)/[A-Za-z0-9_/-]+$"},
                "class": {"enum": ["immutable", "mutable_with_revision", "ephemeral"]},
                "object_class": {"enum": ["immutable", "mutable_with_revision", "ephemeral"]},
                "authoritative": {"type": "boolean"},
                "promotion": {"enum": ["none", "human_stamped"]},
                "tombstone": {"type": "boolean"}
            },
            "anyOf": [{"required": ["class"]}, {"required": ["object_class"]}],
            "allOf": [{"if": {"properties": {"authoritative": {"const": True}}}, "then": {"properties": {"promotion": {"const": "human_stamped"}}}}]
        }
    },
    "obs.evidence.rules": {
        "version": "1.0", "adr": "ADR-0024", "owner": "works-observability",
        "schema": {
            "$id": "contract:obs.evidence.rules/1.0",
            "type": "object",
            "required": ["kind"],
            "properties": {
                "kind": {"enum": ["event", "evidence"]},
                "trimmable": {"type": "boolean"},
                "signed": {"type": "boolean"},
                "cites_hash": {"type": "string"}
            },
            "allOf": [
                {"if": {"properties": {"kind": {"const": "evidence"}}}, "then": {"properties": {"trimmable": {"const": False}, "signed": {"const": True}}}},
                {"if": {"properties": {"kind": {"const": "event"}}}, "then": {"properties": {"signed": {"const": False}}}}
            ]
        }
    },
    "shell.contracts": {
        "version": "1.0", "adr": "ADR-0025", "owner": "works-shell",
        "schema": {
            "$id": "contract:shell.contracts/1.0",
            "type": "object",
            "required": ["surface", "system", "renders", "commands"],
            "properties": {
                "surface": {"enum": ["NOW", "SPACE", "FOCUS", "LIVE", "MEMORY", "COMMAND", "CONTEXT", "SWITCH", "ACT", "MOUNT", "WORKS"]},
                "system": {"enum": ["works", "pulse"]},
                "renders": {"type": "array", "items": {"type": "string"}},
                "commands": {"type": "array", "items": {"enum": ["watch", "approve", "deny", "tell", "stop", "kill", "resume", "run", "cron", "take", "hand_back", "mount", "unmount", "export", "pair", "unpair", "pause", "open", "switch", "pin", "note", "timer", "action", "search", "grant", "revoke", "inspect_evidence"]}},
                "tier": {"enum": ["T1_read", "T2_action", "T3_privileged", "local_only", "none"]},
                "executor": {"const": "works_kernel"}
            },
            "allOf": [
                {"if": {"properties": {"system": {"const": "pulse"}, "tier": {"const": "local_only"}}}, "then": {"properties": {"commands": {"not": {"contains": {"enum": ["kill", "approve", "deny", "take", "hand_back"]}}}}}},
                {"if": {"properties": {"system": {"const": "pulse"}, "tier": {"const": "T3_privileged"}}}, "then": {"properties": {"surface": {"const": "COMMAND"}}}}
            ]
        }
    },
    "link.wire": {
        "version": "1.0", "adr": "ADR-0026", "owner": "pulse-link",
        "schema": {
            "$id": "contract:link.wire/1.0",
            "type": "object",
            "required": ["endpoint", "method", "auth"],
            "properties": {
                "endpoint": {"enum": ["/link/v1/pair", "/link/v1/mounts", "/link/v1/missions", "/link/v1/commands", "/link/v1/revoke"]},
                "method": {"enum": ["POST", "GET"]},
                "auth": {"const": "mTLS+device_token"},
                "idempotency_key": {"type": "string"},
                "payload_hash": {"type": "string"},
                "scope": {"enum": ["T1_read", "T2_action", "T3_privileged"]}
            }
        }
    },
    "pairing": {
        "version": "1.0", "adr": "ADR-0027", "owner": "pulse-link",
        "schema": {
            "$id": "contract:pairing/1.0",
            "type": "object",
            "required": ["state", "device_id"],
            "properties": {
                "state": {"enum": ["UNPAIRED", "PAIRING_REQUEST", "DISPLAY_CODE", "KEY_EXCHANGE", "PAIRED", "RE_PAIR", "REVOKED"]},
                "sas_code": {"type": "string", "pattern": "^[A-Z0-9]{6}$"},
                "device_id": {"type": "string"},
                "scopes": {"type": "array", "items": {"enum": ["T1_read", "T2_action", "T3_privileged"]}},
                "key_store": {"const": "DPAPI"}
            }
        }
    },
    "kernel.lifecycle": {
        "version": "1.0", "adr": "ADR-0009/0010/0014 + closure-doc", "owner": "works-kernel",
        "schema": {
            "$id": "contract:kernel.lifecycle/1.0",
            "type": "object",
            "required": ["machine", "states", "terminals"],
            "properties": {
                "machine": {"enum": ["mission", "runtime", "takeover_baton", "consent", "pairing", "evidence"]},
                "states": {"type": "array", "items": {"type": "string"}},
                "terminals": {"type": "array", "items": {"type": "string"}},
                "invariants": {"type": "array", "items": {"type": "string"}}
            }
        }
    },
    "pulse.db": {
        "version": "1.0", "adr": "ADR-0013 + domain model", "owner": "pulse-core",
        "schema": {
            "$id": "contract:pulse.db/1.0",
            "type": "object",
            "required": ["entities", "consent_rule"],
            "properties": {
                "schema_version": {"type": "integer"},
                "entities": {"type": "array", "items": {"enum": ["PulseContext", "PulseResource", "PulseNote", "PulseTimer", "PulseActivity", "PulseClipboardItem", "PulsePrivacySettings", "ConsentGrant", "ContextMount", "WorksLinkStatus", "PulseEvent"]}, "uniqueItems": True},
                "consent_rule": {"const": "no upload without active ConsentGrant; revoke is locally authoritative"},
                "wal": {"const": True}}
        }
    },
    "release.rings": {
        "version": "1.0", "adr": "ADR-0013 + runbook", "owner": "pulse-release",
        "schema": {
            "$id": "contract:release.rings/1.0",
            "type": "object",
            "required": ["rings"],
            "properties": {
                "rings": {"type": "array", "items": {"enum": ["internal", "alpha", "beta", "stable"]}, "uniqueItems": True},
                "beta_soak_hours": {"const": 48},
                "kill_switch": {"type": "array", "items": {"type": "string"}},
                "no_ring_skips": {"const": True}
            }
        }
    },
}


def main():
    os.makedirs(OUT, exist_ok=True)
    entries = []
    for name, spec in SCHEMAS.items():
        path = os.path.join(OUT, name + ".schema.json")
        with open(path, "w", encoding="utf-8") as f:
            json.dump(spec["schema"], f, indent=2, sort_keys=True)
            f.write("\n")
        digest = hashlib.sha256(open(path, "rb").read()).hexdigest()
        entries.append({
            "contract": name,
            "version": spec["version"],
            "owner": spec["owner"],
            "source_adr": spec["adr"],
            "schema": f"contracts/schemas/{name}.schema.json",
            "sha256": digest,
            "compat": "N-1 read tolerance per proto.charter/1.0; breaking = major bump (ADR-0021)",
        })
    manifest = {
        "manifest_version": "1.0",
        "generated_by": "Contract Freeze Slice 0",
        "authority": "final-freeze-review.md (READY_FOR_CONTRACT_FREEZE) — approved by Jonas",
        "note": "hash over denne fil = freeze-attestation; endringer i schemas ændrer hashen (drift, aldrig stille)",
        "entry_count": len(entries),
        "contracts": entries,
    }
    with open(os.path.join(os.path.dirname(OUT), "manifest.json"), "w", encoding="utf-8") as f:
        json.dump(manifest, f, indent=2, sort_keys=True)
        f.write("\n")
    self_hash = hashlib.sha256(open(os.path.join(os.path.dirname(OUT), "manifest.json"), "rb").read()).hexdigest()
    with open(os.path.join(os.path.dirname(OUT), "manifest.sha256"), "w") as f:
        f.write(self_hash + "\n")
    print(f"manifest entries={len(entries)} manifest_sha256={self_hash}")


if __name__ == "__main__":
    sys.exit(main())