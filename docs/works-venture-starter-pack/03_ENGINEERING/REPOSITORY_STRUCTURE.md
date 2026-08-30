# Proposed Repository Structure

```text
works/
  cmd/
    works/
    works-worker/
  services/
    api/
    work/
    scheduler/
    worker-gateway/
    integration-github/
  packages/
    protocol/
    work-graph/
    evidence/
    policy/
  web/
  infra/
  docs/
    adr/
    product/
    architecture/
    security/
  benchmarks/
  test/
    conformance/
    chaos/
```

Keep the worker protocol and core schemas versioned independently from service implementation.
