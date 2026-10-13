package store

// WORKS G18: read-only HTTP provider evidence transport.
// This adapter deliberately has NO Execute, Submit, RetryEffect, or Apply API.
// All result verification and durable journaling stay in FencedEffectReceiptReader.
import (
 "bytes"
 "context"
 "encoding/json"
 "io"
 "net"
 "net/http"
 "net/url"
 "regexp"
 "strings"
 "time"
)

const readbackMaxBytes = 65536
var readbackKeyPattern = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")

type HTTPProviderReadback struct {
 endpoint string
 bearer string
 client *http.Client
}

// Construct only inside an authenticated server host with a pinned provider
// endpoint and operator-supplied readback credential. Never expose baseURL to
// model inputs. Plain HTTP is allowed solely on the loopback test transport.
func NewHTTPProviderReadback(baseURL, bearer string) (*HTTPProviderReadback,error){
 u,err:=url.Parse(strings.TrimSpace(baseURL))
 if err!=nil||u==nil||u.Host==""||u.User!=nil||u.RawQuery!=""||
  u.Fragment!=""||u.Opaque!=""||(u.Path!=""&&u.Path!="/")||
  u.RawPath!=""||u.ForceQuery||len(bearer)<32||strings.ContainsAny(bearer,"\r\n"){
  return nil,ErrEffectReceiptInvalid
 }
 host:=u.Hostname()
 addr:=net.ParseIP(host)
 loopback:=host=="localhost"||(addr!=nil&&addr.IsLoopback())
 if u.Scheme!="https"&&!(u.Scheme=="http"&&loopback){
  return nil,ErrEffectReceiptInvalid
 }
 return &HTTPProviderReadback{
  endpoint:strings.TrimRight(u.String(),"/")+"/v1/effects/receipts/",
  bearer:bearer,
  client:&http.Client{Timeout:5*time.Second,
   CheckRedirect:func(*http.Request,[]*http.Request)error{return http.ErrUseLastResponse}},
 },nil
}

// LookupEffectReceipt performs exactly one idempotency-keyed HTTP GET.
// HTTP failures, missing receipts and ambiguous replies NEVER trigger a
// mutation or return execution authority.
func(p *HTTPProviderReadback)LookupEffectReceipt(ctx context.Context,key string)(*SignedProviderReceipt,error){
 if p==nil||p.client==nil||p.bearer==""||!readbackKeyPattern.MatchString(key){
  return nil,ErrEffectReceiptInvalid
 }
 req,err:=http.NewRequestWithContext(ctx,http.MethodGet,p.endpoint+url.PathEscape(key),nil)
 if err!=nil{return nil,ErrEffectReceiptUnknown}
 req.Header.Set("Authorization","Bearer "+p.bearer)
 req.Header.Set("Accept","application/json")
 response,err:=p.client.Do(req)
 if err!=nil{return nil,ErrEffectReceiptUnknown}
 defer response.Body.Close()
 if response.StatusCode==http.StatusNotFound{return nil,ErrEffectReceiptUnknown}
 if response.StatusCode!=http.StatusOK{return nil,ErrEffectReceiptUnknown}
 raw,err:=io.ReadAll(io.LimitReader(response.Body,readbackMaxBytes+1))
 if err!=nil||len(raw)>readbackMaxBytes{return nil,ErrEffectReceiptInvalid}
 var wire struct {
  Schema string `json:"schema"`
  Payload ProviderReceipt `json:"payload"`
  Signature []byte `json:"signature"`
 }
 dec:=json.NewDecoder(bytes.NewReader(raw))
 dec.DisallowUnknownFields()
 if dec.Decode(&wire)!=nil||wire.Schema!="aftergraph.provider-readback/1"||
  wire.Payload.IdempotencyKey!=key||len(wire.Signature)!=64{
  return nil,ErrEffectReceiptInvalid
 }
 var trailing any
 if dec.Decode(&trailing)!=io.EOF{return nil,ErrEffectReceiptInvalid}
 return &SignedProviderReceipt{Payload:wire.Payload,Signature:wire.Signature},nil
}
