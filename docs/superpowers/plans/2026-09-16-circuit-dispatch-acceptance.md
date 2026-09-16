# Circuit Dispatch Acceptance Algorithm Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement CDA-0.1 so a CircuitRun dispatch acceptance and its CircuitEffectBinding commit atomically or not at all.

**Architecture:** Add a WORKS-owned composite method on SQLiteStore. It runs the existing dispatch.Acceptor against a transaction-backed dispatch.Store adapter, then creates the T040b effect binding in the same SQL transaction. The frozen dispatch.acceptance/1.0 contract and legacy non-Circuit acceptance path remain unchanged.

**Tech Stack:** Go, database/sql, SQLite, existing internal/dispatch and packages/circuitrun.

**Spec:** Approved in chat on 2026-09-16; CDA-0.1 deterministic atomic acceptance algorithm.

## Global Constraints

- No API endpoint, Runtime integration, production deployment, merge, or frozen dispatch contract change.
- Exact replay is idempotent; causal mismatch and binding conflicts fail closed.
- A failed binding MUST roll back a newly inserted dispatch acceptance.
- Circuit acceptance does not grant authority, prove execution success, or create verification truth.