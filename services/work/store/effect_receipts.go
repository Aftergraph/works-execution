package store

// WORKS G18: signed effect receipts and lookup-only crash recovery.
// This module NEVER submits a provider mutation. Unknown is never retried.
import (
 "bytes"
 "context"
 "crypto/ed25519"
 "crypto/sha256"
 "database/sql"
 "encoding/hex"
 "encoding/json"
 "errors"
 "io"
 "time"
)

var ErrEffectReceiptUnknown=errors.New("works: unknown provider outcome; manual reconciliation required")
var ErrEffectReceiptInvalid=errors.New("works: invalid provider evidence")
var ErrEffectReceiptConflict=errors.New("works: provider evidence identity conflict")

const effectReceiptSchema=`
CREATE TABLE IF NOT EXISTS dispatch_effect_action_bindings (
 idempotency_key TEXT PRIMARY KEY,
 action_id TEXT NOT NULL UNIQUE,
 authority_lease_id TEXT NOT NULL,
 effect_digest TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS dispatch_effect_receipts (
 idempotency_key TEXT PRIMARY KEY,
 works_execution_id TEXT NOT NULL,
 effect_id TEXT NOT NULL,
 provider_id TEXT NOT NULL,
 evidence_id TEXT NOT NULL UNIQUE,
 receipt_json TEXT NOT NULL,
 signature BLOB NOT NULL,
 observed_at TEXT NOT NULL,
 UNIQUE(works_execution_id,effect_id)
);
`

// ProviderReceipt is canonically serialized with json.Marshal before signing.
type ProviderReceipt struct {
 Schema string `json:"schema"`
 ProviderID string `json:"provider_id"`
 OperationID string `json:"operation_id"`
 WorksExecutionID string `json:"works_execution_id"`
 EffectID string `json:"effect_id"`
 IdempotencyKey string `json:"idempotency_key"`
 EffectDigest string `json:"effect_digest"`
 ActionID string `json:"action_id"`
 Amount int64 `json:"amount"`
 Outcome string `json:"outcome"`
 ResultDigest string `json:"result_digest"`
 IssuedAtUnixMs int64 `json:"issued_at_unix_ms"`
}
type SignedProviderReceipt struct {
 Payload ProviderReceipt
 Signature []byte
}
type VerifiedProviderReceipt struct {
 Receipt ProviderReceipt
 EvidenceID string
 Verified bool
}
// ProviderReadback has NO Execute or Apply method by design.
type ProviderReadback interface {
 LookupEffectReceipt(context.Context,string)(*SignedProviderReceipt,error)
}
type FencedEffectReceiptReader struct {
 store *SQLiteStore
 providerID string
 signer ed25519.PublicKey
 lookup ProviderReadback
}
func NewFencedEffectReceiptReader(s *SQLiteStore, providerID string, signer ed25519.PublicKey,
 lookup ProviderReadback)(*FencedEffectReceiptReader,error){
 if s==nil||s.db==nil||providerID==""||len(providerID)>128||
  len(signer)!=ed25519.PublicKeySize||lookup==nil{return nil,ErrEffectReceiptInvalid}
 return &FencedEffectReceiptReader{store:s,providerID:providerID,
 signer:append(ed25519.PublicKey(nil),signer...),lookup:lookup},nil
}
func (r *FencedEffectReceiptReader)sealed(txn *sql.Tx,ctx context.Context,
 in FencedEffectInput)(string,string,error){
 var ec,work,tenant,principal,lease,effect,digest,state string
 var amount int64
 err:=txn.QueryRowContext(ctx,`SELECT execution_context_id,work_id,tenant_id,
 principal_id,worker_lease_id,effect_id,action_digest,amount,state
 FROM dispatch_effect_holds WHERE idempotency_key=? AND works_execution_id=?`,
 in.IdempotencyKey,in.WorksExecutionID).Scan(&ec,&work,&tenant,&principal,
 &lease,&effect,&digest,&amount,&state)
 if err!=nil{
  if errors.Is(err,sql.ErrNoRows){return "","",ErrEffectReceiptConflict}
  return "","",err
 }
 if state!="reconcile"||ec!=in.ExecutionContextID||work!=in.WorkID||
  tenant!=in.TenantID||principal!=in.PrincipalID||lease!=in.WorkerLeaseID||
  effect!=in.EffectID||digest!=in.ActionDigest||amount!=in.Amount{
  return "","",ErrEffectReceiptConflict
 }
 accepted,err:=loadAcceptance(txn,`SELECT works_execution_id,idempotency_key,causal_id,
 acceptance_json FROM dispatch_acceptances WHERE works_execution_id=?`,in.WorksExecutionID)
 if err!=nil{return "","",err}
 if accepted==nil||accepted.ContractVersion!="dispatch.acceptance/2.0"||
 accepted.WorkID!=in.WorkID||accepted.ExecutionContextID!=in.ExecutionContextID||
 accepted.Dispatch.EffectID!=in.EffectID||
 accepted.Dispatch.EffectDigest!=in.ActionDigest||
 !effectActionPattern.MatchString(accepted.Dispatch.ActionID){
  return "","",ErrEffectReceiptConflict
 }
 // Revocation or lease expiration after a provider commit must NOT hide
 // historical receipts. This method is readback only, never authority.
 var effectAction,authorityRef,effectDigest string
 err=txn.QueryRowContext(ctx,`SELECT action_id,authority_lease_id,effect_digest
 FROM dispatch_effect_action_bindings WHERE idempotency_key=?`,
 in.IdempotencyKey).Scan(&effectAction,&authorityRef,&effectDigest)
 if err!=nil||!effectActionPattern.MatchString(effectAction)||
 effectAction==accepted.Dispatch.ActionID||
 authorityRef!=accepted.Dispatch.AuthorityRef||
 effectDigest!=in.ActionDigest{return "","",ErrEffectReceiptConflict}
 return accepted.Dispatch.ActionID,effectAction,nil
}
func(r *FencedEffectReceiptReader)verify(proof *SignedProviderReceipt,
 in FencedEffectInput,acceptAction,effectAction string)(*VerifiedProviderReceipt,error){
 if proof==nil||len(proof.Signature)!=ed25519.SignatureSize{
  return nil,ErrEffectReceiptInvalid
 }
 v:=proof.Payload
 if v.Schema!="aftergraph.works-provider-receipt/1"||
 v.ProviderID!=r.providerID||v.OperationID==""||
 v.WorksExecutionID!=in.WorksExecutionID||v.EffectID!=in.EffectID||
 v.IdempotencyKey!=in.IdempotencyKey||v.EffectDigest!=in.ActionDigest||
 v.Amount!=in.Amount||!effectActionPattern.MatchString(v.ActionID)||
 v.ActionID!=effectAction||v.ActionID==acceptAction||!effectDigestPattern.MatchString(v.ResultDigest)||
 (v.Outcome!="succeeded"&&v.Outcome!="failed")||
 v.IssuedAtUnixMs<=0||v.IssuedAtUnixMs>time.Now().Add(5*time.Minute).UnixMilli(){
  return nil,ErrEffectReceiptInvalid
 }
 canonical,err:=json.Marshal(v)
 if err!=nil||!ed25519.Verify(r.signer,canonical,proof.Signature){
  return nil,ErrEffectReceiptInvalid
 }
 digest:=sha256.Sum256(append(canonical,proof.Signature...))
 return &VerifiedProviderReceipt{Receipt:v,
  EvidenceID:"sha256:"+hex.EncodeToString(digest[:]),Verified:true},nil
}
func (r *FencedEffectReceiptReader)Read(ctx context.Context,in FencedEffectInput)(*VerifiedProviderReceipt,error){
 if r==nil{return nil,ErrEffectReceiptInvalid}
 txn,err:=r.store.db.BeginTx(ctx,&sql.TxOptions{ReadOnly:true})
 if err!=nil{return nil,err}
 defer txn.Rollback()
 accept,effectAction,err:=r.sealed(txn,ctx,in)
 if err!=nil{return nil,err}
 var payload string
 var signature []byte
 var evidenceID,providerID string
 err=txn.QueryRowContext(ctx,`SELECT receipt_json,signature,evidence_id,provider_id
 FROM dispatch_effect_receipts WHERE idempotency_key=?`,in.IdempotencyKey).
 Scan(&payload,&signature,&evidenceID,&providerID)
 if errors.Is(err,sql.ErrNoRows){return nil,ErrEffectReceiptUnknown}
 if err!=nil{return nil,err}
 if providerID!=r.providerID{return nil,ErrEffectReceiptConflict}
 var receipt ProviderReceipt
 decoder:=json.NewDecoder(bytes.NewReader([]byte(payload)))
 decoder.DisallowUnknownFields()
 if err:=decoder.Decode(&receipt);err!=nil{return nil,ErrEffectReceiptInvalid}
 var extra any
 if decoder.Decode(&extra)!=io.EOF{return nil,ErrEffectReceiptInvalid}
 verified,err:=r.verify(&SignedProviderReceipt{Payload:receipt,Signature:signature},in,accept,effectAction)
 if err!=nil||verified.EvidenceID!=evidenceID{return nil,ErrEffectReceiptInvalid}
 if err:=txn.Commit();err!=nil{return nil,err}
 return verified,nil
}
// Reconcile calls only provider LOOKUP with the original key, never Execute.
// A missing or unavailable result remains UNKNOWN, so no resubmission occurs.
func (r *FencedEffectReceiptReader)Reconcile(ctx context.Context,in FencedEffectInput)(*VerifiedProviderReceipt,error){
 if r==nil||r.lookup==nil{return nil,ErrEffectReceiptInvalid}
 previous,err:=r.Read(ctx,in)
 if err==nil{return previous,nil}
 if !errors.Is(err,ErrEffectReceiptUnknown){return nil,err}
 proof,err:=r.lookup.LookupEffectReceipt(ctx,in.IdempotencyKey)
 if err!=nil||proof==nil{return nil,ErrEffectReceiptUnknown}
 txn,err:=r.store.db.BeginTx(ctx,nil)
 if err!=nil{return nil,err}
 defer txn.Rollback()
 accept,effectAction,err:=r.sealed(txn,ctx,in)
 if err!=nil{return nil,err}
 verified,err:=r.verify(proof,in,accept,effectAction)
 if err!=nil{return nil,err}
 canonical,err:=json.Marshal(proof.Payload)
 if err!=nil{return nil,ErrEffectReceiptInvalid}
 _,err=txn.ExecContext(ctx,`INSERT INTO dispatch_effect_receipts
 (idempotency_key,works_execution_id,effect_id,provider_id,evidence_id,receipt_json,signature,observed_at)
 VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(idempotency_key) DO NOTHING`,
 in.IdempotencyKey,in.WorksExecutionID,in.EffectID,r.providerID,verified.EvidenceID,
 string(canonical),proof.Signature,time.Now().UTC().Format(time.RFC3339Nano))
 if err!=nil{return nil,err}
 var existing string
 if err:=txn.QueryRowContext(ctx,`SELECT evidence_id FROM dispatch_effect_receipts
 WHERE idempotency_key=?`,in.IdempotencyKey).Scan(&existing);err!=nil||
 existing!=verified.EvidenceID{return nil,ErrEffectReceiptConflict}
 if err:=txn.Commit();err!=nil{return nil,err}
 return verified,nil
}
