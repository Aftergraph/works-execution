# Durable mission status semantics

`works mission run` returning successfully means submission/reconciliation succeeded and the control plane owns the Work. It is not a VERIFIED verdict.

`CREATED`, `QUEUED`, `RUNNING`, and `VERIFYING` are execution lifecycle states. `WAITING_HUMAN` and `SUSPENDED` require explicit governed resume. `BUDGET_EXHAUSTED` is containment and must never auto-resume. `SUCCEEDED` means WORKS execution completed; independent mission verification remains a separate acceptance boundary.
