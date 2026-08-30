# ADR-0002: Control Plane owns authoritative state

**Status:** Accepted

Workers are disposable executors with leases. They cannot unilaterally declare authoritative Work success. Terminal state is committed by the control plane after validating attempt/result/evidence.
