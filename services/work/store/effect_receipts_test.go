package store_test

import (
 "context"
 "crypto/ed25519"
 "crypto/rand"
 "crypto/sha256"
 "database/sql"
 "encoding/hex"
 "encoding/json"
 "errors"
 "fmt"
 "path/filepath"
 "strings"
 "testing"
 "time"

 workstore "github.com/JonasAbde/works-execution/services/work/store"
)

const sandboxProviderID="aftergraph.sandbox.counter/1"
type sandboxProvider struct {
 db *sql.DB
 signer ed25519.PrivateKey
 lookups int
 applyCalls int
 offline bool
 corrupt bool
}
func newSandboxProvider(t *testing.T)*sandboxProvider{
 t.Helper()
 db,err:=sql.Open("sqlite","file:"+filepath.Join(t.TempDir(),"provider.sqlite"))
 if err!=nil{t.Fatal(err)}
 db.SetMaxOpenConns(1)
 _,err=db.Exec(`
 CREATE TABLE sandbox_counter (id INTEGER PRIMARY KEY,value INTEGER NOT NULL);
 INSERT INTO sandbox_counter(id,value) VALUES(1,0);
 CREATE TABLE sandbox_receipts(idempotency_key TEXT PRIMARY KEY,payload_json TEXT NOT NULL,signature BLOB NOT NULL);
 `)
 if err!=nil{t.Fatal(err)}
 _,priv,err:=ed25519.GenerateKey(rand.Reader)
 if err!=nil{t.Fatal(err)}
 t.Cleanup(func(){db.Close()})
 return &sandboxProvider{db:db,signer:priv}
}
// Apply exists ONLY in this test file, never the production readback API.
func(p *sandboxProvider)Apply(ctx context.Context,in workstore.FencedEffectInput,action string)error{
 p.applyCalls++
 tx,err:=p.db.BeginTx(ctx,nil)
 if err!=nil{return err}
 defer tx.Rollback()
 var duplicate string
 err=tx.QueryRowContext(ctx,`SELECT payload_json FROM sandbox_receipts WHERE idempotency_key=?`,in.IdempotencyKey).Scan(&duplicate)
 if err==nil{return tx.Commit()}
 if !errors.Is(err,sql.ErrNoRows){return err}
 var value int
 if err=tx.QueryRowContext(ctx,`SELECT value FROM sandbox_counter WHERE id=1`).Scan(&value);err!=nil{return err}
 value++
 summary:=sha256.Sum256([]byte(fmt.Sprintf("sandbox-counter:%s:%d",in.IdempotencyKey,value)))
 receipt:=workstore.ProviderReceipt{
  Schema:"aftergraph.works-provider-receipt/1",
  ProviderID:sandboxProviderID,OperationID:"sandbox-op-1",
  WorksExecutionID:in.WorksExecutionID,EffectID:in.EffectID,
  IdempotencyKey:in.IdempotencyKey,EffectDigest:in.ActionDigest,
  ActionID:action,Amount:in.Amount,Outcome:"succeeded",
  ResultDigest:"sha256:"+hex.EncodeToString(summary[:]),
  IssuedAtUnixMs:time.Now().UnixMilli(),
 }
 payload,err:=json.Marshal(receipt)
 if err!=nil{return err}
 signature:=ed25519.Sign(p.signer,payload)
 if _,err=tx.ExecContext(ctx,`UPDATE sandbox_counter SET value=? WHERE id=1`,value);err!=nil{return err}
 if _,err=tx.ExecContext(ctx,`INSERT INTO sandbox_receipts(idempotency_key,payload_json,signature)
 VALUES(?,?,?)`,in.IdempotencyKey,string(payload),signature);err!=nil{return err}
 return tx.Commit()
}
func(p *sandboxProvider)LookupEffectReceipt(ctx context.Context,key string)(*workstore.SignedProviderReceipt,error){
 p.lookups++
 if p.offline{return nil,errors.New("provider unavailable")}
 var payload string
 var signature []byte
 err:=p.db.QueryRowContext(ctx,`SELECT payload_json,signature FROM sandbox_receipts WHERE idempotency_key=?`,key).Scan(&payload,&signature)
 if err!=nil{return nil,err}
 var data workstore.ProviderReceipt
 if err=json.Unmarshal([]byte(payload),&data);err!=nil{return nil,err}
 if p.corrupt{data.EffectDigest="sha256:"+strings.Repeat("f",64)}
 return &workstore.SignedProviderReceipt{Payload:data,Signature:signature},nil
}
func(p *sandboxProvider)count(t *testing.T)int{
 t.Helper()
 var n int
 if err:=p.db.QueryRow("SELECT value FROM sandbox_counter WHERE id=1").Scan(&n);err!=nil{t.Fatal(err)}
 return n
}
func TestSignedProviderRecoveryAfterUnknownCommittedSandboxOutcome(t *testing.T){
 s,in,done:=fencedFixture(t);defer done()
 ctx:=context.Background()
 provider:=newSandboxProvider(t)
 reader,err:=workstore.NewFencedEffectReceiptReader(s,sandboxProviderID,
  provider.signer.Public().(ed25519.PublicKey),provider)
 if err!=nil{t.Fatal(err)}
 prepared,err:=s.PrepareFencedEffect(ctx,
  workstore.FencedEffectPreparationInput{Hold:in,ActionID:effectAction},
  effectAuthorityFunc(func(context.Context,workstore.EffectTimeScope)error{return nil}))
 if err!=nil||prepared.CanDispatchEffect{t.Fatalf("preflight: %+v %v",prepared,err)}
 // Simulate a provider that COMMITTED but the reply was lost.
 if err=provider.Apply(ctx,in,effectAction);err!=nil{t.Fatal(err)}
 if provider.count(t)!=1{t.Fatal("provider mutation did not persist")}
 if _,err=reader.Read(ctx,in);!errors.Is(err,workstore.ErrEffectReceiptUnknown){
  t.Fatalf("WORKS invented a receipt: %v",err)
 }
 recovered,err:=reader.Reconcile(ctx,in)
 if err!=nil||!recovered.Verified||recovered.Receipt.Outcome!="succeeded"{
  t.Fatalf("recovery invalid: %+v %v",recovered,err)
 }
 if !strings.HasPrefix(recovered.EvidenceID,"sha256:"){t.Fatal("missing evidence ID")}
 calls:=provider.lookups
 again,err:=reader.Reconcile(ctx,in)
 if err!=nil||again.EvidenceID!=recovered.EvidenceID||provider.lookups!=calls{
  t.Fatalf("unexpected lookup/replay: %+v %v",again,err)
 }
 if provider.count(t)!=1||provider.applyCalls!=1{t.Fatal("effect repeated")}
 if err=s.ReleaseLease(ctx,in.WorkerLeaseID,"expired");err!=nil{t.Fatal(err)}
 if _,err=reader.Read(ctx,in);err!=nil{t.Fatalf("revocation erased evidence: %v",err)}
}
func TestProviderOutcomeUnknownDoesNotExecuteAgain(t *testing.T){
 s,in,done:=fencedFixture(t);defer done()
 ctx:=context.Background()
 provider:=newSandboxProvider(t)
 reader,err:=workstore.NewFencedEffectReceiptReader(s,sandboxProviderID,
  provider.signer.Public().(ed25519.PublicKey),provider)
 if err!=nil{t.Fatal(err)}
 if _,err=s.PrepareFencedEffect(ctx,workstore.FencedEffectPreparationInput{Hold:in,ActionID:effectAction},
  effectAuthorityFunc(func(context.Context,workstore.EffectTimeScope)error{return nil}));err!=nil{t.Fatal(err)}
 provider.offline=true
 if _,err=reader.Reconcile(ctx,in);!errors.Is(err,workstore.ErrEffectReceiptUnknown){
  t.Fatalf("outage was treated as success: %v",err)
 }
 if provider.applyCalls!=0||provider.count(t)!=0{t.Fatal("reconcile dispatched effect on outage")}
}
func TestProviderForgedReceiptAndWrongSignerFailClosed(t *testing.T){
 s,in,done:=fencedFixture(t);defer done()
 ctx:=context.Background()
 provider:=newSandboxProvider(t)
 if _,err:=s.PrepareFencedEffect(ctx,workstore.FencedEffectPreparationInput{Hold:in,ActionID:effectAction},
  effectAuthorityFunc(func(context.Context,workstore.EffectTimeScope)error{return nil}));err!=nil{t.Fatal(err)}
 if err:=provider.Apply(ctx,in,effectAction);err!=nil{t.Fatal(err)}
 correct,err:=workstore.NewFencedEffectReceiptReader(s,sandboxProviderID,
  provider.signer.Public().(ed25519.PublicKey),provider)
 if err!=nil{t.Fatal(err)}
 provider.corrupt=true
 if _,err=correct.Reconcile(ctx,in);!errors.Is(err,workstore.ErrEffectReceiptInvalid){
  t.Fatalf("invalid provider signature accepted: %v",err)
 }
 provider.corrupt=false
 _,other,err:=ed25519.GenerateKey(rand.Reader)
 if err!=nil{t.Fatal(err)}
 wrong,err:=workstore.NewFencedEffectReceiptReader(s,sandboxProviderID,
  other.Public().(ed25519.PublicKey),provider)
 if err!=nil{t.Fatal(err)}
 if _,err=wrong.Reconcile(ctx,in);!errors.Is(err,workstore.ErrEffectReceiptInvalid){
  t.Fatalf("untrusted signer accepted: %v",err)
 }
 if _,err=correct.Read(ctx,in);!errors.Is(err,workstore.ErrEffectReceiptUnknown){
  t.Fatalf("failed evidence left a receipt: %v",err)
 }
}
func TestProviderReceiptMismatchAndUnsealedIntentsCannotReconcile(t *testing.T){
 s,in,done:=fencedFixture(t);defer done()
 ctx:=context.Background()
 provider:=newSandboxProvider(t)
 r,err:=workstore.NewFencedEffectReceiptReader(s,sandboxProviderID,
  provider.signer.Public().(ed25519.PublicKey),provider)
 if err!=nil{t.Fatal(err)}
 if _,err=r.Reconcile(ctx,in);!errors.Is(err,workstore.ErrEffectReceiptConflict){
  t.Fatalf("unsealed intent accepted: %v",err)
 }
 if _,err=s.PrepareFencedEffect(ctx,workstore.FencedEffectPreparationInput{Hold:in,ActionID:effectAction},
  effectAuthorityFunc(func(context.Context,workstore.EffectTimeScope)error{return nil}));err!=nil{t.Fatal(err)}
 if err=provider.Apply(ctx,in,effectAction);err!=nil{t.Fatal(err)}
 wrong:=in;wrong.PrincipalID="prn_invalid"
 if _,err=r.Reconcile(ctx,wrong);!errors.Is(err,workstore.ErrEffectReceiptConflict){
  t.Fatalf("wrong principal read a receipt: %v",err)
 }
 if _,err=r.Reconcile(ctx,in);err!=nil{t.Fatal(err)}
 if provider.count(t)!=1{t.Fatal("duplicate sandbox mutation")}
}

func TestSignedReceiptCannotRebindDifferentEffectAction(t *testing.T){
 s,in,done:=fencedFixture(t);defer done()
 ctx:=context.Background()
 provider:=newSandboxProvider(t)
 reader,err:=workstore.NewFencedEffectReceiptReader(s,sandboxProviderID,
  provider.signer.Public().(ed25519.PublicKey),provider)
 if err!=nil{t.Fatal(err)}
 if _,err=s.PrepareFencedEffect(ctx,workstore.FencedEffectPreparationInput{Hold:in,ActionID:effectAction},
  effectAuthorityFunc(func(context.Context,workstore.EffectTimeScope)error{return nil}));err!=nil{t.Fatal(err)}
 if err=provider.Apply(ctx,in,"act_"+strings.Repeat("c",32));err!=nil{t.Fatal(err)}
 if _,err=reader.Reconcile(ctx,in);!errors.Is(err,workstore.ErrEffectReceiptInvalid){
  t.Fatalf("cross-action signed receipt accepted: %v",err)
 }
 if _,err=reader.Read(ctx,in);!errors.Is(err,workstore.ErrEffectReceiptUnknown){
  t.Fatalf("cross-action evidence persisted: %v",err)
 }
}
