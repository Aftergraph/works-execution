# HC7 Evidence Verification + HC8 Takeover Continuity

## Summary
Implemented WORKS HC7 evidence verification and HC8 human takeover continuity.

## HC7: Evidence Verification

### Files Created
- `services/evidence/verify.go` - Evidence bundle verification entry point
- `services/evidence/verify_test.go` - Tests marked for CI

### Implementation Details
`VerifyBundle(bundle, keyID, hmacKey)` validates:
1. **HMAC-SHA256 signature** - Re-canonicalizes bundle (with placeholder bundle_id and stripped signatures), computes HMAC, compares in constant time
2. **Content-addressed bundle_id** - Re-derives bundle_id as `evb_` + sha256(canonicalJSON)[:32hex]
3. **Correlation-ID completeness** - Checks presence of:
   - Bundle-level: bundle_id, work_id
   - Attempts: id, node_id, lease_id
   - Evidence: id, node_id, attempt_id

### API
```go
func VerifyBundle(b *Bundle, keyID string, hmacKey []byte) (*BundleVerificationResult, error)
func VerifyBundleSimple(b *Bundle, keyID string, hmacKey []byte) bool
```

## HC8: Human Takeover Continuity

### Files Created
- `internal/worker/takeover.go` - Takeover handling
- `internal/worker/takeover_test.go` - Tests marked for CI

### Implementation Details
`TakeoverHandler.HandleTakeover(ctx, req)` ensures:
1. **AIE revalidation** - Lease re-validated via AIE revalidate hook (fail-closed)
2. **Evidence chain** - Creates takeover_event evidence entry with:
   - Link to original lease
   - Original and new worker IDs
   - Permissions granted
3. **No authority escalation** - Permissions must be subset of original worker's permissions

### API
```go
func NewTakeoverHandler(revalidateHook func(ctx context.Context, leaseID string, workerID string) error) *TakeoverHandler
func (h *TakeoverHandler) HandleTakeover(ctx context.Context, req TakeoverRequest) *TakeoverResult
func (h *TakeoverHandler) RecordPermissions(workerID string, permissions []string)
```

## Tests
Tests are marked for CI in:
- `services/evidence/verify_test.go`
- `internal/worker/takeover_test.go`

## Commit
SHA: 3148426
Pushed to origin/main
