package store_test

import(
 "context"
 "encoding/json"
 "errors"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"

 "github.com/JonasAbde/works-execution/internal/dispatch"
 workstore "github.com/JonasAbde/works-execution/services/work/store"
)

const effectAction="act_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type effectAuthorityFunc func(context.Context,workstore.EffectTimeScope)error
func(f effectAuthorityFunc)RevalidateFencedEffect(ctx context.Context,s workstore.EffectTimeScope)error{
 return f(ctx,s)
}
func TestFencedEffectPreflightUsesPersistedScopeAndSealsOnce(t *testing.T){
 s,in,done:=fencedFixture(t);defer done()
 calls:=0
 revalidate:=effectAuthorityFunc(func(_ context.Context,b workstore.EffectTimeScope)error{
  calls++
  if b.ActionID!=effectAction||
   b.AuthorityLeaseID!="auth_"+strings.Repeat("d",32)||
   b.MissionID!="mis_12345"||
   b.EffectDigest!=in.ActionDigest||b.Amount!=3||
   b.WorksExecutionID!=in.WorksExecutionID||
   b.ExecutionContextID!=in.ExecutionContextID||
   b.WorkerLeaseID!=in.WorkerLeaseID||
   b.TenantID!=in.TenantID||b.PrincipalID!=in.PrincipalID{
   t.Fatalf("not canonical WORKS identity: %+v",b)
  }
  return nil
 })
 prepared,err:=s.PrepareFencedEffect(context.Background(),
  workstore.FencedEffectPreparationInput{Hold:in,ActionID:effectAction},revalidate)
 if err!=nil{t.Fatal(err)}
 if prepared.Status!="reconciliation_required"||prepared.CanDispatchEffect||
  calls!=1{t.Fatalf("unsafe prepared result: %+v, calls=%d",prepared,calls)}
 _,err=s.PrepareFencedEffect(context.Background(),
  workstore.FencedEffectPreparationInput{Hold:in,ActionID:effectAction},revalidate)
 if !errors.Is(err,workstore.ErrEffectRequiresReconciliation)||calls!=1{
  t.Fatalf("replay revalidated a sealed effect: %v calls=%d",err,calls)
 }
}
func TestFencedEffectPreflightRejectsTrustOutageBeforeSeal(t *testing.T){
 s,in,done:=fencedFixture(t);defer done()
 denied:=effectAuthorityFunc(func(context.Context,workstore.EffectTimeScope)error{
  return errors.New("AIE revoked or offline")
 })
 _,err:=s.PrepareFencedEffect(context.Background(),
  workstore.FencedEffectPreparationInput{Hold:in,ActionID:effectAction},denied)
 if !errors.Is(err,workstore.ErrEffectTimeAuthorityDenied){t.Fatal(err)}
 hold,err:=s.HoldFencedEffect(context.Background(),in)
 if err!=nil||hold.State!="held"{t.Fatalf("denied Trust sealed intent: %+v %v",hold,err)}
}
func TestFencedEffectPreflightRejectsLeaseRevokedDuringAIECall(t *testing.T){
 s,in,done:=fencedFixture(t);defer done()
 revoke:=effectAuthorityFunc(func(ctx context.Context,_ workstore.EffectTimeScope)error{
  return s.ReleaseLease(ctx,in.WorkerLeaseID,"revoked_during_revalidation")
 })
 _,err:=s.PrepareFencedEffect(context.Background(),
  workstore.FencedEffectPreparationInput{Hold:in,ActionID:effectAction},revoke)
 if !errors.Is(err,workstore.ErrFencedEffectDenied){t.Fatalf("stale lease sealed: %v",err)}
}
func TestFencedEffectPreflightRejectsAcceptanceRevokedDuringAIECall(t *testing.T){
 s,in,done:=fencedFixture(t);defer done()
 revoke:=effectAuthorityFunc(func(ctx context.Context,_ workstore.EffectTimeScope)error{
  return dispatch.NewAcceptor(s.DispatchAcceptanceV2Store(in.WorkID),nil).
   Revoke(in.WorksExecutionID)
 })
 _,err:=s.PrepareFencedEffect(context.Background(),
  workstore.FencedEffectPreparationInput{Hold:in,ActionID:effectAction},revoke)
 if !errors.Is(err,workstore.ErrFencedEffectDenied){t.Fatalf("revoked acceptance sealed: %v",err)}
}
func TestFencedEffectPreflightRejectsMissingAuthorityAndMalformedAction(t *testing.T){
 s,in,done:=fencedFixture(t);defer done()
 for _,id:=range []string{"","act_invalid","act_"+strings.Repeat("z",32)}{
  _,err:=s.PrepareFencedEffect(context.Background(),
   workstore.FencedEffectPreparationInput{Hold:in,ActionID:id},nil)
  if !errors.Is(err,workstore.ErrEffectTimeAuthorityDenied){t.Fatal(err)}
 }
}

func effectScopeFixture()workstore.EffectTimeScope{
 return workstore.EffectTimeScope{
  ActionID:effectAction,AuthorityLeaseID:"auth_"+strings.Repeat("a",32),
  TenantID:"ten_"+strings.Repeat("1",32),PrincipalID:"prn_"+strings.Repeat("2",32),
  MissionID:"mis_test",WorkID:"wrk_"+strings.Repeat("3",32),
  WorksExecutionID:"wex_test",ExecutionContextID:"ctx_"+strings.Repeat("4",32),
  WorkerLeaseID:"lse_"+strings.Repeat("5",32),AttemptID:"attempt_one",
  EffectID:"effect_one",IdempotencyKey:"idem_one",
  EffectDigest:"sha256:"+strings.Repeat("a",64),Amount:3,
 }
}
func TestTrustGatewayEffectTimeRealHTTPAndFalseAuthorityFlag(t *testing.T){
 scope:=effectScopeFixture()
 token:=strings.Repeat("x",32)
 srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  if r.Method!="POST"||r.URL.Path!="/v2/works/effects/revalidate"||
   r.Header.Get("Authorization")!="Bearer "+token{
   t.Errorf("untrusted transport");w.WriteHeader(403);return
  }
  var request map[string]any
  if err:=json.NewDecoder(r.Body).Decode(&request);err!=nil{t.Fatal(err)}
  if request["schema"]!="aftergraph.works-effect-revalidation-request/1"||
   request["effectDigest"]!=scope.EffectDigest||
   request["actionId"]!=scope.ActionID||
   request["worksExecutionId"]!=scope.WorksExecutionID{
   t.Errorf("missing effect binding: %+v",request);w.WriteHeader(403);return
  }
  _=json.NewEncoder(w).Encode(map[string]any{
   "schema":"aftergraph.works-effect-revalidation/1","allowed":true,
   "actionId":scope.ActionID,"authorityLeaseId":scope.AuthorityLeaseID,
   "effectDigest":scope.EffectDigest,"worksExecutionId":scope.WorksExecutionID,
   "executionContextId":scope.ExecutionContextID,
   "effectId":scope.EffectID,"idempotencyKey":scope.IdempotencyKey,
   "amount":scope.Amount,"canDispatchEffect":false,
  })
 }))
 defer srv.Close()
 c,err:=workstore.NewTrustGatewayEffectTimeAuthority(srv.URL,token)
 if err!=nil{t.Fatal(err)}
 if err=c.RevalidateFencedEffect(context.Background(),scope);err!=nil{t.Fatal(err)}
}
func TestTrustGatewayEffectTimeDeniesRedirectForgedAndUnavailableResponses(t *testing.T){
 scope:=effectScopeFixture()
 token:=strings.Repeat("x",32)
 for _,body:=range []string{
  `{"schema":"aftergraph.works-effect-revalidation/1","allowed":true}`,
  `{"schema":"aftergraph.works-effect-revalidation/1","allowed":true,"actionId":"`+effectAction+`","authorityGranted":true}`,
 }{
  server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
   _,_=w.Write([]byte(body))
  }))
  c,err:=workstore.NewTrustGatewayEffectTimeAuthority(server.URL,token)
  if err!=nil{t.Fatal(err)}
  if err:=c.RevalidateFencedEffect(context.Background(),scope);err==nil{
   t.Fatal("forged Trust response was accepted")
  }
  server.Close()
 }
 redirect:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  http.Redirect(w,r,"http://example.com",http.StatusTemporaryRedirect)
 }))
 defer redirect.Close()
 c,err:=workstore.NewTrustGatewayEffectTimeAuthority(redirect.URL,token)
 if err!=nil{t.Fatal(err)}
 if err:=c.RevalidateFencedEffect(context.Background(),scope);err==nil{
  t.Fatal("redirect was followed")
 }
 for _,host:=range []string{"http://example.com","http://user:pass@localhost:1","https://trust.invalid/path"}{
  if _,err:=workstore.NewTrustGatewayEffectTimeAuthority(host,token);err==nil{
   t.Fatalf("untrusted configuration allowed: %s",host)
  }
 }
}

func TestTrustGatewayEffectTimeRejectsConcatenatedPolicyResponses(t *testing.T){
 scope:=effectScopeFixture()
 token:=strings.Repeat("x",32)
 server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  valid:=map[string]any{
   "schema":"aftergraph.works-effect-revalidation/1",
   "allowed":true,"actionId":scope.ActionID,"authorityLeaseId":scope.AuthorityLeaseID,
   "effectDigest":scope.EffectDigest,"worksExecutionId":scope.WorksExecutionID,
   "executionContextId":scope.ExecutionContextID,
   "effectId":scope.EffectID,"idempotencyKey":scope.IdempotencyKey,
   "amount":scope.Amount,"canDispatchEffect":false,
  }
  b,_:=json.Marshal(valid)
  _,_=w.Write(append(b,[]byte("\n{\"allowed\":true}")...))
 }))
 defer server.Close()
 client,err:=workstore.NewTrustGatewayEffectTimeAuthority(server.URL,token)
 if err!=nil{t.Fatal(err)}
 if err=client.RevalidateFencedEffect(context.Background(),scope);err==nil{
  t.Fatal("concatenated positive Trust observations must fail closed")
 }
}
