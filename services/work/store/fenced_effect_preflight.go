package store

// G18 #2095: live effect-time AIE preflight + existing canonical WORKS holds.
// This does NOT execute a provider, issue an effect grant or claim success.
import (
 "context"
 "errors"
 "regexp"
)

var ErrEffectTimeAuthorityDenied=errors.New("works: live AIE effect-time authority denied")
var ErrEffectRequiresReconciliation=errors.New("works: existing effect requires reconciliation")
var effectActionPattern=regexp.MustCompile("^act_[a-f0-9]{32}$")

type FencedEffectPreparationInput struct{
 Hold FencedEffectInput
 ActionID string
}
type EffectTimeScope struct{
 ActionID string
 AuthorityLeaseID string
 TenantID string
 PrincipalID string
 MissionID string
 WorkID string
 WorksExecutionID string
 ExecutionContextID string
 WorkerLeaseID string
 AttemptID string
 EffectID string
 IdempotencyKey string
 EffectDigest string
 Amount int64
}
type EffectTimeAuthority interface{
 RevalidateFencedEffect(context.Context,EffectTimeScope)error
}
type PreparedEffectIntent struct{
 Status string
 WorksExecutionID string
 EffectID string
 IdempotencyKey string
 CanDispatchEffect bool
}

// PrepareFencedEffect is server-internal only. Hold is durable and atomic,
// and the ACTUAL persisted WORKS acceptance supplies mission + authority
// identity. A separately admitted effect action is revalidated against
// AIE immediately before sealing. Sealing rechecks live WORKS lease.
// Unknown outcomes remain reconciliation-required, NEVER auto-replayed.
func (s *SQLiteStore) PrepareFencedEffect(ctx context.Context,in FencedEffectPreparationInput,authority EffectTimeAuthority)(*PreparedEffectIntent,error){
 if authority==nil||!effectActionPattern.MatchString(in.ActionID)||
  in.Hold.ActionDigest==""{
  return nil,ErrEffectTimeAuthorityDenied
 }
 hold,err:=s.HoldFencedEffect(ctx,in.Hold)
 if err!=nil{return nil,err}
 if hold==nil||hold.State!="held"{
  return nil,ErrEffectRequiresReconciliation
 }
 // The canonical acceptance is the sole source for the authority lease,
 // mission and worker attempt; none is copied from model-supplied claims.
 acc,err:=loadAcceptance(s.db,"SELECT works_execution_id,idempotency_key,causal_id,"+
  "acceptance_json FROM dispatch_acceptances WHERE works_execution_id=?",in.Hold.WorksExecutionID)
 if err!=nil{return nil,err}
 if acc==nil||acc.ContractVersion!="dispatch.acceptance/2.0"||
  acc.Revoked||acc.EffectApplied||acc.Outcome!="ACCEPTED"||
  acc.WorkID!=in.Hold.WorkID||acc.ExecutionContextID!=in.Hold.ExecutionContextID||
  acc.Dispatch.EffectID!=in.Hold.EffectID||
  acc.Dispatch.AuthorityRef==""||acc.Dispatch.MissionID==""||
  acc.Dispatch.AttemptID==""{
  return nil,ErrEffectTimeAuthorityDenied
 }
 scope:=EffectTimeScope{
  ActionID:in.ActionID,AuthorityLeaseID:acc.Dispatch.AuthorityRef,
  TenantID:in.Hold.TenantID,PrincipalID:in.Hold.PrincipalID,
  MissionID:acc.Dispatch.MissionID,WorkID:acc.WorkID,
  WorksExecutionID:in.Hold.WorksExecutionID,
  ExecutionContextID:in.Hold.ExecutionContextID,
  WorkerLeaseID:in.Hold.WorkerLeaseID,
  AttemptID:acc.Dispatch.AttemptID,
  EffectID:in.Hold.EffectID,IdempotencyKey:in.Hold.IdempotencyKey,
  EffectDigest:in.Hold.ActionDigest,Amount:in.Hold.Amount,
 }
 if err:=authority.RevalidateFencedEffect(ctx,scope);err!=nil{
  // Do not automatically release this hold: an ambiguous caller outcome
  // must be reconciled by the canonical owner, not spent or replayed.
  return nil,ErrEffectTimeAuthorityDenied
 }
 if err:=s.sealFencedEffect(ctx,in.Hold,&scope);err!=nil{return nil,err}
 return &PreparedEffectIntent{
  Status:"reconciliation_required",
  WorksExecutionID:in.Hold.WorksExecutionID,
  EffectID:in.Hold.EffectID,
  IdempotencyKey:in.Hold.IdempotencyKey,
  CanDispatchEffect:false,
 },nil
}
