package store_test

import(
 "context"
 "crypto/ed25519"
 "encoding/json"
 "errors"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"

 workstore "github.com/JonasAbde/works-execution/services/work/store"
)

// Actual localhost HTTP GET, signed by the SEPARATE sandbox SQLite provider.
// Its Apply method exists only in _test.go; production WORKS only has GET.
func TestHTTPProviderReadbackRecoversCommittedLostResponseOnce(t *testing.T){
 s,in,done:=fencedFixture(t);defer done()
 ctx:=context.Background()
 provider:=newSandboxProvider(t)
 if _,err:=s.PrepareFencedEffect(ctx,workstore.FencedEffectPreparationInput{Hold:in,ActionID:effectAction},
  effectAuthorityFunc(func(context.Context,workstore.EffectTimeScope)error{return nil}));err!=nil{t.Fatal(err)}
 if err:=provider.Apply(ctx,in,effectAction);err!=nil{t.Fatal(err)}
 token:=strings.Repeat("s",48)
 requests:=0
 sandbox:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  requests++
  if r.Method!=http.MethodGet||r.URL.Path!="/v1/effects/receipts/"+in.IdempotencyKey||
   r.Header.Get("Authorization")!="Bearer "+token{
    t.Errorf("readback-only HTTP contract violated: method=%s path=%s",r.Method,r.URL.Path)
    w.WriteHeader(http.StatusForbidden);return
  }
  got,err:=provider.LookupEffectReceipt(r.Context(),in.IdempotencyKey)
  if err!=nil{w.WriteHeader(http.StatusNotFound);return}
  w.Header().Set("Content-Type","application/json")
  _=json.NewEncoder(w).Encode(map[string]any{
   "schema":"aftergraph.provider-readback/1","payload":got.Payload,
   "signature":got.Signature,
  })
 }))
 defer sandbox.Close()
 readback,err:=workstore.NewHTTPProviderReadback(sandbox.URL,token)
 if err!=nil{t.Fatal(err)}
 reader,err:=workstore.NewFencedEffectReceiptReader(s,sandboxProviderID,
  provider.signer.Public().(ed25519.PublicKey),readback)
 if err!=nil{t.Fatal(err)}
 proof,err:=reader.Reconcile(ctx,in)
 if err!=nil||!proof.Verified||proof.Receipt.Outcome!="succeeded"{
  t.Fatalf("HTTP signed reconciliation failed: %+v %v",proof,err)
 }
 if requests!=1||provider.applyCalls!=1||provider.count(t)!=1{
  t.Fatalf("unintended duplicate: HTTP=%d Apply=%d",requests,provider.applyCalls)
 }
 proof2,err:=reader.Reconcile(ctx,in)
 if err!=nil||proof2.EvidenceID!=proof.EvidenceID||requests!=1{
  t.Fatalf("replayed HTTP lookup after evidence persisted: %+v %v",proof2,err)
 }
}
func TestHTTPProviderReadbackInvalidAndUnreachableAreNotExecution(t *testing.T){
 ctx:=context.Background()
 token:=strings.Repeat("s",48)
 for _,url:=range []string{
  "http://example.com","http://user:secret@localhost:9090",
  "https://example.com/path","https://example.com?flag=1",
  "http://localhost:9090/?q=1","", "ftp://localhost",
 }{
  if _,err:=workstore.NewHTTPProviderReadback(url,token);err==nil{
   t.Fatalf("accepted unsafe provider origin: %q",url)
  }
 }
 if _,err:=workstore.NewHTTPProviderReadback("http://localhost:9090","too_short");err==nil{
  t.Fatal("accepted missing operator credential")
 }
 cases:=[]struct{name string;status int;body string}{
  {"missing",http.StatusNotFound,""},
  {"unavailable",http.StatusServiceUnavailable,""},
  {"extra fields",http.StatusOK,`{"schema":"aftergraph.provider-readback/1","payload":{},"signature":"","allowed":true}`},
  {"non canonical",http.StatusOK,`{"schema":"wrong","payload":{},"signature":""}`},
  {"oversize",http.StatusOK,strings.Repeat("x",66000)},
  {"second JSON",http.StatusOK,`{"schema":"aftergraph.provider-readback/1","payload":{},"signature":""}
{"allowed":true}`},
  {"redirect",http.StatusTemporaryRedirect,""},
 }
 for _,tc:=range cases{
  t.Run(tc.name,func(t *testing.T){
   calls:=0
   server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
    calls++
    if r.Method!=http.MethodGet{
     t.Errorf("readback used mutation method %s",r.Method)
    }
    if tc.status==http.StatusTemporaryRedirect{
     w.Header().Set("Location","http://example.com")
    }
    w.WriteHeader(tc.status)
    _,_=w.Write([]byte(tc.body))
   }))
   defer server.Close()
   readback,err:=workstore.NewHTTPProviderReadback(server.URL,token)
   if err!=nil{t.Fatal(err)}
   if _,err=readback.LookupEffectReceipt(ctx,"effect-idem-1");err==nil{
    t.Fatal("untrusted or unavailable response was accepted")
   }else if !errors.Is(err,workstore.ErrEffectReceiptUnknown)&&
    !errors.Is(err,workstore.ErrEffectReceiptInvalid){
    t.Fatalf("unexpected error: %v",err)
   }
   if calls!=1{t.Fatalf("followed redirect or retried GET %d times",calls)}
  })
 }
}
