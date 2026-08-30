# Scheduler Design

## Hard constraints
- OS / architecture
- required runtime
- CPU / memory / GPU
- network class
- trust class
- tenant/pool
- region/data residency
- secret/deployment authority

## Soft optimization
Eligible workers are scored using:
- cache locality
- expected runtime
- queue pressure
- current utilization
- cost
- network proximity
- reliability history

## V1
Use deterministic, explainable heuristics.

## Later
Use historical duration and failure probability models. Machine learning must never override hard policy constraints.

## Explainability
Every assignment stores a decision record:
- eligible worker count
- rejected constraints
- selected worker
- score components
- fallback reason if degraded
