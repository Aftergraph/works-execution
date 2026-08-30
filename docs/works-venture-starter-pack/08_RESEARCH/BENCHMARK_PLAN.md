# Benchmark Plan

## Workloads
- cold build
- warm repeat build
- small affected PR
- large PR
- flaky test
- worker loss
- OOM/reschedule
- Docker-heavy build
- burst concurrency
- agent rapid-iteration loop

## Baselines
- GitHub-hosted Actions
- relevant GitHub self-hosted setup
- Depot
- Buildkite
- Nx Cloud for task-graph/monorepo workloads

## Required disclosure
Repository revision, machine shape, region, cache state, concurrency, repetitions, median/P95, cost assumptions, network conditions and whether a number is independently measured or vendor-provided.

## 100x rule
Never claim universal 100x runtime. If the venture uses "100x", define it as an ambition for verified-work throughput and publish the exact dimensions.
