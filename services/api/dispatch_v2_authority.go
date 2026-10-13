package api

// WORKS #106: accept-time AIE revalidation via the *existing* Trust Gateway.
// The caller provides correlation identities, never a trusted decision.
// A successful result permits only the durable V2 acceptance. It is NOT
// permission to invoke an external effect.
import (
 "bytes"
 "context"
 "encoding/json"
 "errors"
 "io"
 "net"
 "net/http"
 "net/url"
 "regexp"
 "strings"
 "time"
)

var ErrDispatchV2AuthorityUnavailable=errors.New("dispatch v2 authority unavailable or stale")
var actionIDPattern=regexp.MustCompile("^act_[a-f0-9]{32}$")
var effectDigestPatternV2=regexp.MustCompile("^sha256:[a-f0-9]{64}$")

type DispatchV2AuthorityRequest struct {
 Schema string `json:"schema"`
 ActionID string `json:"actionId"`
 AuthorityLeaseID string `json:"authorityLeaseId"`
 OrganizationID string `json:"organizationId"`
 TenantID string `json:"tenantId"`
 PrincipalID string `json:"principalId"`
 MissionID string `json:"missionId"`
 WorkID string `json:"workId"`
 WorkerLeaseID string `json:"workerLeaseId"`
 RuntimeDispatchID string `json:"runtimeDispatchId"`
 AttemptID string `json:"attemptId"`
 EffectID string `json:"effectId"`
 IdempotencyKey string `json:"idempotencyKey"`
 BudgetCeiling int64 `json:"budgetCeiling"`
 EffectDigest string `json:"effectDigest"`
}
type DispatchV2Authority interface {
 RevalidateDispatchV2(context.Context,DispatchV2AuthorityRequest) error
}

type TrustGatewayDispatchV2Authority struct{
 endpoint string
 bearer string
 client *http.Client
}
func NewTrustGatewayDispatchV2Authority(rawURL,token string)(*TrustGatewayDispatchV2Authority,error){
 u,err:=url.Parse(strings.TrimSpace(rawURL))
 if err!=nil||u.Host==""||u.User!=nil||u.RawQuery!=""||u.Fragment!=""||u.Path!=""{
  return nil,ErrDispatchV2AuthorityUnavailable
 }
 host:=u.Hostname()
 loopbackIP:=net.ParseIP(host)
 isLoopback:=host=="localhost"||(loopbackIP!=nil&&loopbackIP.IsLoopback())
 if u.Scheme!="https"&&!(u.Scheme=="http"&&isLoopback){
  return nil,ErrDispatchV2AuthorityUnavailable
 }
 if len(token)<32{return nil,ErrDispatchV2AuthorityUnavailable}
 return &TrustGatewayDispatchV2Authority{endpoint:strings.TrimRight(u.String(),"/")+
 "/v2/works/dispatch/revalidate",bearer:token,client:&http.Client{Timeout:5*time.Second}},nil
}
func (a *TrustGatewayDispatchV2Authority) RevalidateDispatchV2(ctx context.Context,in DispatchV2AuthorityRequest)error{
 if a==nil||a.client==nil||a.bearer==""||
  in.Schema!="aftergraph.works-dispatch-revalidation-request/1"||
  !actionIDPattern.MatchString(in.ActionID)||
  !effectDigestPatternV2.MatchString(in.EffectDigest){
  return ErrDispatchV2AuthorityUnavailable
 }
 payload,err:=json.Marshal(in)
 if err!=nil{return ErrDispatchV2AuthorityUnavailable}
 req,err:=http.NewRequestWithContext(ctx,http.MethodPost,a.endpoint,bytes.NewReader(payload))
 if err!=nil{return ErrDispatchV2AuthorityUnavailable}
 req.Header.Set("Content-Type","application/json")
 req.Header.Set("Authorization","Bearer "+a.bearer)
 res,err:=a.client.Do(req)
 if err!=nil{return ErrDispatchV2AuthorityUnavailable}
 defer res.Body.Close()
 if res.StatusCode!=http.StatusOK{return ErrDispatchV2AuthorityUnavailable}
 data,err:=io.ReadAll(io.LimitReader(res.Body,4097))
 if err!=nil||len(data)>4096{return ErrDispatchV2AuthorityUnavailable}
 var out struct{
  Schema string `json:"schema"`
  Allowed bool `json:"allowed"`
  ActionID string `json:"actionId"`
  AuthorityLeaseID string `json:"authorityLeaseId"`
  EffectDigest string `json:"effectDigest"`
  CanDispatchEffect bool `json:"canDispatchEffect"`
 }
 if err:=json.Unmarshal(data,&out);err!=nil||
  out.Schema!="aftergraph.works-dispatch-revalidation/1"||
  !out.Allowed||out.CanDispatchEffect||out.ActionID!=in.ActionID||
  out.AuthorityLeaseID!=in.AuthorityLeaseID||out.EffectDigest!=in.EffectDigest{
  return ErrDispatchV2AuthorityUnavailable
 }
 return nil
}
