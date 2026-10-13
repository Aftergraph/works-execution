package store

// A server-owned WORKS→Trust/AIE effect-time client. Not an execution token.
import(
 "bytes"
 "context"
 "encoding/json"
 "io"
 "net"
 "net/http"
 "net/url"
 "strings"
 "time"
)

type TrustGatewayEffectTimeAuthority struct{
 endpoint,token string
 client *http.Client
}
func NewTrustGatewayEffectTimeAuthority(rawURL,operatorToken string)(*TrustGatewayEffectTimeAuthority,error){
 u,err:=url.Parse(strings.TrimSpace(rawURL))
 if err!=nil||u.Host==""||u.User!=nil||u.Fragment!=""||
  u.RawQuery!=""||u.Path!=""||len(operatorToken)<32{
  return nil,ErrEffectTimeAuthorityDenied
 }
 host:=u.Hostname()
 parsed:=net.ParseIP(host)
 if u.Scheme!="https"&&!(u.Scheme=="http"&&(host=="localhost"||
  (parsed!=nil&&parsed.IsLoopback()))){
  return nil,ErrEffectTimeAuthorityDenied
 }
 c:=&http.Client{Timeout:5*time.Second,
  CheckRedirect:func(*http.Request,[]*http.Request)error{return http.ErrUseLastResponse}}
 return &TrustGatewayEffectTimeAuthority{endpoint:strings.TrimRight(u.String(),"/")+
  "/v2/works/effects/revalidate",token:operatorToken,client:c},nil
}
func (t *TrustGatewayEffectTimeAuthority)RevalidateFencedEffect(ctx context.Context,s EffectTimeScope)error{
 if t==nil||t.client==nil||len(t.token)<32||!effectActionPattern.MatchString(s.ActionID)||
  !effectDigestPattern.MatchString(s.EffectDigest)||s.Amount<=0{
  return ErrEffectTimeAuthorityDenied
 }
 request:=map[string]any{
  "schema":"aftergraph.works-effect-revalidation-request/1",
  "actionId":s.ActionID,"authorityLeaseId":s.AuthorityLeaseID,
  "tenantId":s.TenantID,"principalId":s.PrincipalID,
  "missionId":s.MissionID,"workId":s.WorkID,
  "worksExecutionId":s.WorksExecutionID,"executionContextId":s.ExecutionContextID,
  "workerLeaseId":s.WorkerLeaseID,"attemptId":s.AttemptID,
  "effectId":s.EffectID,"idempotencyKey":s.IdempotencyKey,
  "effectDigest":s.EffectDigest,"amount":s.Amount,
 }
 encoded,err:=json.Marshal(request)
 if err!=nil{return ErrEffectTimeAuthorityDenied}
 req,err:=http.NewRequestWithContext(ctx,http.MethodPost,t.endpoint,bytes.NewReader(encoded))
 if err!=nil{return ErrEffectTimeAuthorityDenied}
 req.Header.Set("Content-Type","application/json")
 req.Header.Set("Authorization","Bearer "+t.token)
 response,err:=t.client.Do(req)
 if err!=nil{return ErrEffectTimeAuthorityDenied}
 defer response.Body.Close()
 if response.StatusCode!=http.StatusOK{return ErrEffectTimeAuthorityDenied}
 content,err:=io.ReadAll(io.LimitReader(response.Body,4097))
 if err!=nil||len(content)>4096{return ErrEffectTimeAuthorityDenied}
 var result struct{
  Schema string
  Allowed bool
  ActionID string
  AuthorityLeaseID string
  EffectDigest string
  WorksExecutionID string
  ExecutionContextID string
  EffectID string
  IdempotencyKey string
  Amount int64
  CanDispatchEffect *bool
 }
 decoder:=json.NewDecoder(bytes.NewReader(content))
 decoder.DisallowUnknownFields()
 if err:=decoder.Decode(&result);err!=nil||
  result.Schema!="aftergraph.works-effect-revalidation/1"||
  !result.Allowed||result.CanDispatchEffect==nil||*result.CanDispatchEffect||
  result.ActionID!=s.ActionID||result.AuthorityLeaseID!=s.AuthorityLeaseID||
  result.EffectDigest!=s.EffectDigest||result.WorksExecutionID!=s.WorksExecutionID||
  result.ExecutionContextID!=s.ExecutionContextID||result.EffectID!=s.EffectID||
  result.IdempotencyKey!=s.IdempotencyKey||result.Amount!=s.Amount{
  return ErrEffectTimeAuthorityDenied
 }
 return nil
}
