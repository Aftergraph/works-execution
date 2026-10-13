package store

// Canonical WORKS budget escrow and single-use effect intent.
// This is a durable reservation, NEVER a live Trust/AIE authorization.
import (
 "context"
 "database/sql"
 "errors"
 "fmt"
 "regexp"
 "time"

 "github.com/JonasAbde/works-execution/internal/dispatch"
)

var ErrFencedEffectDenied=errors.New("works: fenced effect denied")
var ErrFencedEffectReplay=errors.New("works: conflicting effect replay")
var effectDigestPattern=regexp.MustCompile("^sha256:[a-f0-9]{64}$")

type FencedEffectInput struct {
 WorksExecutionID string
 ExecutionContextID string
 WorkID string
 TenantID string
 PrincipalID string
 WorkerLeaseID string
 EffectID string
 ActionDigest string
 IdempotencyKey string
 Amount int64
}
type FencedEffectHold struct {
 IdempotencyKey string
 WorksExecutionID string
 EffectID string
 ActionDigest string
 Amount int64
 State string
}

const fencedEffectTable = "CREATE TABLE IF NOT EXISTS dispatch_effect_holds ("+
 "idempotency_key TEXT PRIMARY KEY,"+
 "works_execution_id TEXT NOT NULL,"+
 "execution_context_id TEXT NOT NULL,"+
 "work_id TEXT NOT NULL,"+
 "tenant_id TEXT NOT NULL,"+
 "principal_id TEXT NOT NULL,"+
 "worker_lease_id TEXT NOT NULL,"+
 "effect_id TEXT NOT NULL,"+
 "action_digest TEXT NOT NULL,"+
 "amount INTEGER NOT NULL CHECK(amount>0),"+
 "state TEXT NOT NULL CHECK(state IN ('held','reconcile','cancelled')),"+
 "created_at TEXT NOT NULL,"+
 "UNIQUE(works_execution_id,effect_id));"+
 "CREATE INDEX IF NOT EXISTS idx_dispatch_effect_holds_budget "+
 "ON dispatch_effect_holds(works_execution_id,state);"

// HoldFencedEffect checks the ACTUAL persisted V2 acceptance, immutable
// execution context and live worker lease in one WORKS SQLite transaction.
// It reserves against the accepted budget ceiling, without dispatching.
// A replay can recover the same hold, but never an execution permit.
func (s *SQLiteStore) HoldFencedEffect(ctx context.Context,in FencedEffectInput)(*FencedEffectHold,error){
 if in.WorksExecutionID==""||in.ExecutionContextID==""||in.WorkID==""||
 in.TenantID==""||in.PrincipalID==""||in.WorkerLeaseID==""||
 in.EffectID==""||in.IdempotencyKey==""||in.Amount<=0||
 !effectDigestPattern.MatchString(in.ActionDigest){return nil,ErrFencedEffectDenied}
 txn,err:=s.db.BeginTx(ctx,nil)
 if err!=nil{return nil,err}
 defer txn.Rollback()
 var h FencedEffectHold
 var contextID,workID,tenantID,principalID,leaseID string
 err=txn.QueryRowContext(ctx,"SELECT works_execution_id,execution_context_id,work_id,"+
 "tenant_id,principal_id,worker_lease_id,effect_id,action_digest,amount,state "+
 "FROM dispatch_effect_holds WHERE idempotency_key=?",in.IdempotencyKey).
 Scan(&h.WorksExecutionID,&contextID,&workID,&tenantID,&principalID,
 &leaseID,&h.EffectID,&h.ActionDigest,&h.Amount,&h.State)
 if err==nil{
  if h.WorksExecutionID!=in.WorksExecutionID||contextID!=in.ExecutionContextID||
   workID!=in.WorkID||tenantID!=in.TenantID||principalID!=in.PrincipalID||
   leaseID!=in.WorkerLeaseID||h.EffectID!=in.EffectID||
   h.ActionDigest!=in.ActionDigest||h.Amount!=in.Amount{
    return nil,ErrFencedEffectReplay
  }
  h.IdempotencyKey=in.IdempotencyKey
  return &h,nil
 }
 if !errors.Is(err,sql.ErrNoRows){return nil,err}
 acc,err:=loadAcceptance(txn,"SELECT works_execution_id,idempotency_key,causal_id,"+
 "acceptance_json FROM dispatch_acceptances WHERE works_execution_id=?",in.WorksExecutionID)
 if err!=nil{return nil,err}
 if acc==nil||acc.ContractVersion!="dispatch.acceptance/2.0"||
 acc.Revoked||acc.EffectApplied||acc.Outcome!="ACCEPTED"||
 acc.WorkID!=in.WorkID||acc.ExecutionContextID!=in.ExecutionContextID||
 acc.Dispatch.EffectID!=in.EffectID||acc.Dispatch.EffectDigest!=in.ActionDigest||
 !effectActionPattern.MatchString(acc.Dispatch.ActionID)||acc.Dispatch.BudgetCeiling<0||
 acc.BudgetSpent<0||acc.BudgetSpent>acc.Dispatch.BudgetCeiling{
  return nil,ErrFencedEffectDenied
 }
 var cw,ct,cp,cl,ca,cm string
 err=txn.QueryRowContext(ctx,"SELECT work_id,tenant_id,principal_id,"+
 "worker_lease_id,authority_lease_id,mission_id "+
 "FROM work_execution_contexts WHERE id=?",in.ExecutionContextID).
 Scan(&cw,&ct,&cp,&cl,&ca,&cm)
 if err!=nil{
  if errors.Is(err,sql.ErrNoRows){return nil,ErrFencedEffectDenied}
  return nil,err
 }
 if cw!=in.WorkID||ct!=in.TenantID||cp!=in.PrincipalID||
 cl!=in.WorkerLeaseID||ca!=acc.Dispatch.AuthorityRef||
 cm!=acc.Dispatch.MissionID{return nil,ErrFencedEffectDenied}
 var lw,la,ls,expires string
 err=txn.QueryRowContext(ctx,"SELECT work_id,attempt_id,status,expires_at "+
 "FROM work_leases WHERE id=?",in.WorkerLeaseID).Scan(&lw,&la,&ls,&expires)
 if err!=nil{
  if errors.Is(err,sql.ErrNoRows){return nil,ErrFencedEffectDenied}
  return nil,err
 }
 expiry,err:=time.Parse(time.RFC3339Nano,expires)
 if err!=nil||lw!=in.WorkID||la!=acc.Dispatch.AttemptID||
 ls!="ACTIVE"||!expiry.After(time.Now().UTC()){return nil,ErrFencedEffectDenied}
 var reserved int64
 err=txn.QueryRowContext(ctx,"SELECT COALESCE(SUM(amount),0) FROM dispatch_effect_holds "+
 "WHERE works_execution_id=? AND state IN ('held','reconcile')",
 in.WorksExecutionID).Scan(&reserved)
 if err!=nil{return nil,err}
 remaining:=acc.Dispatch.BudgetCeiling-acc.BudgetSpent
 if reserved<0||reserved>remaining||in.Amount>remaining-reserved{
  return nil,dispatch.ErrBudgetExhausted
 }
 _,err=txn.ExecContext(ctx,"INSERT INTO dispatch_effect_holds "+
 "(idempotency_key,works_execution_id,execution_context_id,work_id,tenant_id,"+
 "principal_id,worker_lease_id,effect_id,action_digest,amount,state,created_at) "+
 "VALUES (?,?,?,?,?,?,?,?,?,?,'held',?)",in.IdempotencyKey,in.WorksExecutionID,
 in.ExecutionContextID,in.WorkID,in.TenantID,in.PrincipalID,in.WorkerLeaseID,
 in.EffectID,in.ActionDigest,in.Amount,time.Now().UTC().Format(time.RFC3339Nano))
 if err!=nil{return nil,fmt.Errorf("%w: %v",ErrFencedEffectDenied,err)}
 if err=txn.Commit();err!=nil{return nil,err}
 return &FencedEffectHold{IdempotencyKey:in.IdempotencyKey,
 WorksExecutionID:in.WorksExecutionID,EffectID:in.EffectID,
 ActionDigest:in.ActionDigest,Amount:in.Amount,State:"held"},nil
}

// SealFencedEffect seals the hold BEFORE any external tool call. If a
// process dies, a replay sees 'reconcile', never a second dispatch pass.
// Only a separate trusted host with LIVE Trust/AIE authorization may actually
// invoke the provider using its own idempotency key.
func(s *SQLiteStore) SealFencedEffect(ctx context.Context,in FencedEffectInput)error{
 return s.sealFencedEffect(ctx,in,nil)
}
// Scoped sealing pins the same immutable authority and mission observed
// by Trust/AIE. The comparison is repeated inside this final SQLite txn.
func(s *SQLiteStore) sealFencedEffect(ctx context.Context,in FencedEffectInput,scope *EffectTimeScope)error{
 if in.IdempotencyKey==""||in.WorksExecutionID==""||in.ExecutionContextID==""||
 in.WorkID==""||in.WorkerLeaseID==""||in.TenantID==""||
 in.PrincipalID==""||in.EffectID==""||in.Amount<=0||
 !effectDigestPattern.MatchString(in.ActionDigest){return ErrFencedEffectDenied}
 txn,err:=s.db.BeginTx(ctx,nil)
 if err!=nil{return err}
 defer txn.Rollback()
 var stored FencedEffectHold
 var ec,work,tenant,principal,lease string
 err=txn.QueryRowContext(ctx,"SELECT works_execution_id,execution_context_id,work_id,"+
 "tenant_id,principal_id,worker_lease_id,effect_id,action_digest,amount,state "+
 "FROM dispatch_effect_holds WHERE idempotency_key=?",in.IdempotencyKey).
 Scan(&stored.WorksExecutionID,&ec,&work,&tenant,&principal,&lease,
 &stored.EffectID,&stored.ActionDigest,&stored.Amount,&stored.State)
 if err!=nil{
  if errors.Is(err,sql.ErrNoRows){return ErrFencedEffectDenied}
  return err
 }
 if stored.State!="held"||stored.WorksExecutionID!=in.WorksExecutionID||
 ec!=in.ExecutionContextID||work!=in.WorkID||tenant!=in.TenantID||
 principal!=in.PrincipalID||lease!=in.WorkerLeaseID||
 stored.EffectID!=in.EffectID||stored.ActionDigest!=in.ActionDigest||
 stored.Amount!=in.Amount{return ErrFencedEffectDenied}
 acc,err:=loadAcceptance(txn,"SELECT works_execution_id,idempotency_key,causal_id,"+
 "acceptance_json FROM dispatch_acceptances WHERE works_execution_id=?",in.WorksExecutionID)
 if err!=nil{return err}
 if acc==nil||acc.ContractVersion!="dispatch.acceptance/2.0"||
 acc.Revoked||acc.EffectApplied||acc.Outcome!="ACCEPTED"||
 acc.WorkID!=in.WorkID||acc.ExecutionContextID!=in.ExecutionContextID||
 acc.Dispatch.EffectID!=in.EffectID||acc.Dispatch.EffectDigest!=in.ActionDigest||
 !effectActionPattern.MatchString(acc.Dispatch.ActionID){return ErrFencedEffectDenied}
 if scope!=nil && (scope.ActionID==acc.Dispatch.ActionID||scope.WorkID!=in.WorkID||
 scope.WorksExecutionID!=in.WorksExecutionID||
 scope.ExecutionContextID!=in.ExecutionContextID||
 scope.WorkerLeaseID!=in.WorkerLeaseID||
 scope.TenantID!=in.TenantID||scope.PrincipalID!=in.PrincipalID||
 scope.EffectID!=in.EffectID||scope.IdempotencyKey!=in.IdempotencyKey||
 scope.EffectDigest!=in.ActionDigest||scope.Amount!=in.Amount||
 acc.Dispatch.AuthorityRef!=scope.AuthorityLeaseID||
 acc.Dispatch.MissionID!=scope.MissionID||
 acc.Dispatch.AttemptID!=scope.AttemptID) {
  return ErrFencedEffectDenied
 }
 // Independently repeat the minted context-to-acceptance and lease binding.
 var cw,ct,cp,cl,ca,cm string
 err=txn.QueryRowContext(ctx,"SELECT work_id,tenant_id,principal_id,"+
 "worker_lease_id,authority_lease_id,mission_id FROM work_execution_contexts "+
 "WHERE id=?",in.ExecutionContextID).Scan(&cw,&ct,&cp,&cl,&ca,&cm)
 if err!=nil{return ErrFencedEffectDenied}
 if cw!=in.WorkID||ct!=in.TenantID||cp!=in.PrincipalID||
 cl!=in.WorkerLeaseID||ca!=acc.Dispatch.AuthorityRef||
 cm!=acc.Dispatch.MissionID{return ErrFencedEffectDenied}
 var lw,la,ls,lExpires string
 err=txn.QueryRowContext(ctx,"SELECT work_id,attempt_id,status,expires_at "+
 "FROM work_leases WHERE id=?",in.WorkerLeaseID).
 Scan(&lw,&la,&ls,&lExpires)
 if err!=nil{
  if errors.Is(err,sql.ErrNoRows){return ErrFencedEffectDenied}
  return err
 }
 expiry,err:=time.Parse(time.RFC3339Nano,lExpires)
 if err!=nil||lw!=in.WorkID||la!=acc.Dispatch.AttemptID||
 ls!="ACTIVE"||!expiry.After(time.Now().UTC()){return ErrFencedEffectDenied}
 // Pin the effect-specific AIE action atomically with the seal.
 if scope!=nil{
  _,err=txn.ExecContext(ctx,`INSERT INTO dispatch_effect_action_bindings
  (idempotency_key,action_id,authority_lease_id,effect_digest)
  VALUES (?,?,?,?)`,in.IdempotencyKey,scope.ActionID,
   scope.AuthorityLeaseID,scope.EffectDigest)
  if err!=nil{return ErrFencedEffectDenied}
 }
 r,err:=txn.ExecContext(ctx,"UPDATE dispatch_effect_holds SET state='reconcile' "+
 "WHERE idempotency_key=? AND state='held'",in.IdempotencyKey)
 if err!=nil{return err}
 n,err:=r.RowsAffected()
 if err!=nil{return err}
 if n!=1{return ErrFencedEffectDenied}
 return txn.Commit()
}
