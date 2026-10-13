package store_test

import (
 "context"
 "errors"
 "path/filepath"
 "sync"
 "strings"
 "testing"
 "time"

 "github.com/JonasAbde/works-execution/internal/dispatch"
 "github.com/JonasAbde/works-execution/packages/workgraph"
 workstore "github.com/JonasAbde/works-execution/services/work/store"
)

func fencedFixture(t *testing.T)(*workstore.SQLiteStore,workstore.FencedEffectInput,func()){
 t.Helper()
 file:=filepath.Join(t.TempDir(),"works.db")
 s,err:=workstore.Open(file)
 if err!=nil{t.Fatal(err)}
 ctx:=context.Background()
 work:=&workgraph.Work{
  ID:workgraph.NewID("wrk"),State:workgraph.StateCreated,
  Source:workgraph.Source{Type:"cli"},
  Objective:workgraph.Objective{Type:"verify_change"},
  Graph:workgraph.Graph{Nodes:map[string]workgraph.Node{
   "only":{ID:"only",Run:"echo ok"},
  }},
  Requirements:workgraph.Requirements{OS:"linux"},
  Policy:workgraph.Policy{},
 }
 if err=s.CreateWork(ctx,work);err!=nil{t.Fatal(err)}
 if _,err=s.UpdateState(ctx,work.ID,workgraph.StateQueued);err!=nil{t.Fatal(err)}
 lease,attempt,err:=s.GrantLease(ctx,work.ID,"only","wrkr_"+strings.Repeat("f",32),time.Minute)
 if err!=nil{t.Fatal(err)}
 d:=dispatch.Dispatch{
  MissionID:"mis_12345",AuthorityRef:"auth_"+strings.Repeat("d",32),AuthorityEpoch:3,
  RuntimeDispatchID:"runtime-dispatch-1",AttemptID:attempt.ID,
  EffectID:"effect-1",IdempotencyKey:"dispatch-1",BudgetRef:"budget-1",
  BudgetCeiling:5,CheckpointID:"checkpoint-1",EvidenceRoot:"evidence-1",
  VerificationSubj:"subject-1",CausalID:"causal-1",
 }
 binding:=dispatch.V2Binding{
  WorkID:work.ID,OrganizationID:"org_"+strings.Repeat("a",32),TenantID:"ten_"+strings.Repeat("b",32),
  PrincipalID:"prn_"+strings.Repeat("c",32),AuthorityLeaseID:d.AuthorityRef,
  WorkerLeaseID:lease.ID,AdmissionDecisionID:"pdr_"+strings.Repeat("e",32),
 }
 accepted,execution,err:=dispatch.NewAcceptor(s.DispatchAcceptanceV2Store(work.ID),nil).
  AcceptV2(ctx,d,binding)
 if err!=nil{t.Fatal(err)}
 input:=workstore.FencedEffectInput{
  WorksExecutionID:accepted.WorksExecutionID,
  ExecutionContextID:execution.ID,WorkID:work.ID,
  TenantID:binding.TenantID,PrincipalID:binding.PrincipalID,
  WorkerLeaseID:lease.ID,EffectID:d.EffectID,
  ActionDigest:"sha256:"+repeatDigest(),IdempotencyKey:"effect-idem-1",
  Amount:3,
 }
 return s,input,func(){_ = s.Close()}
}
func repeatDigest()string{
 const digits="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
 return digits
}
func TestFencedHoldPersistsAndIsNotExecutionPermission(t *testing.T){
 s,in,done:=fencedFixture(t);defer done()
 got,err:=s.HoldFencedEffect(context.Background(),in)
 if err!=nil{t.Fatal(err)}
 if got.State!="held"||got.Amount!=3{t.Fatalf("unexpected hold %+v",got)}
 read,err:=s.HoldFencedEffect(context.Background(),in)
 if err!=nil||read.IdempotencyKey!=got.IdempotencyKey{t.Fatalf("idempotent recovery: %+v %v",read,err)}
 if err:=s.SealFencedEffect(context.Background(),in);err!=nil{t.Fatal(err)}
 if err:=s.SealFencedEffect(context.Background(),in);!errors.Is(err,workstore.ErrFencedEffectDenied){
  t.Fatalf("second seal must fail: %v",err)
 }
 r,err:=s.HoldFencedEffect(context.Background(),in)
 if err!=nil||r.State!="reconcile"{t.Fatalf("reconcile replay state: %+v %v",r,err)}
}
func TestFencedHoldRejectsWrongIdentityLeaseOrBudget(t *testing.T){
 s,in,done:=fencedFixture(t);defer done()
 ctx:=context.Background()
 for _,edit:=range []func(*workstore.FencedEffectInput){
  func(x *workstore.FencedEffectInput){x.TenantID="another"},
  func(x *workstore.FencedEffectInput){x.PrincipalID="another"},
  func(x *workstore.FencedEffectInput){x.ExecutionContextID="another"},
  func(x *workstore.FencedEffectInput){x.WorkerLeaseID="another"},
  func(x *workstore.FencedEffectInput){x.EffectID="another"},
  func(x *workstore.FencedEffectInput){x.Amount=6},
 }{
  x:=in;edit(&x)
  if _,err:=s.HoldFencedEffect(ctx,x);err==nil{t.Fatalf("unexpected accept %+v",x)}
 }
 good,err:=s.HoldFencedEffect(ctx,in);if err!=nil{t.Fatal(err)}
 changed:=in;changed.ActionDigest="sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
 if _,err:=s.HoldFencedEffect(ctx,changed);!errors.Is(err,workstore.ErrFencedEffectReplay){
  t.Fatalf("conflicting replay: %v (%+v)",err,good)
 }
 differentKey:=in;differentKey.IdempotencyKey="different-id"
 if _,err:=s.HoldFencedEffect(ctx,differentKey);err==nil{
  t.Fatal("same effect cannot create another hold through new key")
 }
}
func TestFencedHoldAfterRevokeOrLostLeaseDenied(t *testing.T){
 s,in,done:=fencedFixture(t);defer done()
 ctx:=context.Background()
 if err:=s.ReleaseLease(ctx,in.WorkerLeaseID,"test");err!=nil{t.Fatal(err)}
 if _,err:=s.HoldFencedEffect(ctx,in);!errors.Is(err,workstore.ErrFencedEffectDenied){
  t.Fatalf("released lease: %v",err)
 }
}
func TestFencedHoldIsAtomicUnderParallelReplay(t *testing.T){
 s,in,done:=fencedFixture(t);defer done()
 const workers=12
 var wg sync.WaitGroup
 wg.Add(workers)
 results:=make(chan error,workers)
 for i:=0;i<workers;i++{
  go func(){
   defer wg.Done()
   _,err:=s.HoldFencedEffect(context.Background(),in)
   results<-err
  }()
 }
 wg.Wait();close(results)
 for err:=range results{if err!=nil{t.Fatal(err)}}
 var n int
 if err:=s.DB().QueryRow("SELECT COUNT(*) FROM dispatch_effect_holds").Scan(&n);err!=nil{t.Fatal(err)}
 if n!=1{t.Fatalf("expected one durable hold got %d",n)}
}

func TestLegacySpendCannotOversubscribeFencedHold(t *testing.T){
 s,in,done:=fencedFixture(t);defer done()
 if _,err:=s.HoldFencedEffect(context.Background(),in);err!=nil{t.Fatal(err)}
 a:=dispatch.NewAcceptor(s.DispatchAcceptanceV2Store(in.WorkID),nil)
 if err:=a.Spend(in.WorksExecutionID,3);err==nil{
  t.Fatal("Spend bypassed already held budget")
 }
 if err:=a.Spend(in.WorksExecutionID,2);err!=nil{
  t.Fatalf("remaining available spend should be allowed: %v",err)
 }
}
func TestFencedHoldDeniedWhenAcceptanceRevoked(t *testing.T){
 s,in,done:=fencedFixture(t);defer done()
 a:=dispatch.NewAcceptor(s.DispatchAcceptanceV2Store(in.WorkID),nil)
 if err:=a.Revoke(in.WorksExecutionID);err!=nil{t.Fatal(err)}
 if _,err:=s.HoldFencedEffect(context.Background(),in);!errors.Is(err,workstore.ErrFencedEffectDenied){
  t.Fatalf("revoked: %v",err)
 }
}

func TestSealDeniedAfterLeaseReleaseOrChangedIdentity(t *testing.T){
 s,in,done:=fencedFixture(t);defer done()
 ctx:=context.Background()
 if _,err:=s.HoldFencedEffect(ctx,in);err!=nil{t.Fatal(err)}
 changed:=in;changed.PrincipalID="other"
 if err:=s.SealFencedEffect(ctx,changed);!errors.Is(err,workstore.ErrFencedEffectDenied){
  t.Fatalf("different principal sealed: %v",err)
 }
 if err:=s.ReleaseLease(ctx,in.WorkerLeaseID,"test");err!=nil{t.Fatal(err)}
 if err:=s.SealFencedEffect(ctx,in);!errors.Is(err,workstore.ErrFencedEffectDenied){
  t.Fatalf("released lease sealed: %v",err)
 }
}
