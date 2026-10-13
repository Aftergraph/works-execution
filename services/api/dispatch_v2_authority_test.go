package api_test

import (
 "context"
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"

 "github.com/JonasAbde/works-execution/services/api"
)

func trustRequest() api.DispatchV2AuthorityRequest{
 return api.DispatchV2AuthorityRequest{
 Schema:"aftergraph.works-dispatch-revalidation-request/1",
 ActionID:"act_"+strings.Repeat("a",32),
 AuthorityLeaseID:"auth_"+strings.Repeat("b",32),
 OrganizationID:"org_"+strings.Repeat("1",32),
 TenantID:"ten_"+strings.Repeat("2",32),
 PrincipalID:"prn_"+strings.Repeat("3",32),
 MissionID:"mis_test",WorkID:"wrk_"+strings.Repeat("4",32),
 WorkerLeaseID:"lse_"+strings.Repeat("5",32),
 RuntimeDispatchID:"rdisp_one",AttemptID:"attempt_one",
 EffectID:"effect_one",IdempotencyKey:"idem_one",BudgetCeiling:12,
 EffectDigest:"sha256:"+strings.Repeat("d",64),
 }
}

func TestTrustGatewayDispatchV2HTTPClientRequiresLiveBoundResponse(t *testing.T){
 input:=trustRequest()
 token:=strings.Repeat("x",32)
 allowed:=true
 server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  if r.URL.Path!="/v2/works/dispatch/revalidate"||
   r.Header.Get("Authorization")!="Bearer "+token||
   r.Method!=http.MethodPost{
    t.Errorf("unexpected trusted transport");w.WriteHeader(403);return
  }
  var data api.DispatchV2AuthorityRequest
  if err:=json.NewDecoder(r.Body).Decode(&data);err!=nil{t.Error(err);w.WriteHeader(400);return}
  if data!=input{t.Errorf("request binding mismatch: %+v",data);w.WriteHeader(403);return}
  if !allowed{w.WriteHeader(503);return}
  json.NewEncoder(w).Encode(map[string]any{
   "schema":"aftergraph.works-dispatch-revalidation/1",
   "allowed":true,"actionId":data.ActionID,
   "authorityLeaseId":data.AuthorityLeaseID,
   "effectDigest":data.EffectDigest,"canDispatchEffect":false,
  })
 }))
 defer server.Close()
 client,err:=api.NewTrustGatewayDispatchV2Authority(server.URL,token)
 if err!=nil{t.Fatal(err)}
 if err:=client.RevalidateDispatchV2(context.Background(),input);err!=nil{t.Fatal(err)}
 allowed=false
 if err:=client.RevalidateDispatchV2(context.Background(),input);err==nil{
  t.Fatal("Trust outage permitted dispatch")
 }
}
func TestTrustGatewayDispatchV2ClientRejectsSpoofedResultsAndBadURLs(t *testing.T){
 for _,u:=range []string{"http://example.com","http://user:pass@localhost:1234","https://trust.invalid/path",""}{
  if _,err:=api.NewTrustGatewayDispatchV2Authority(u,strings.Repeat("x",32));err==nil{
   t.Fatalf("insecure URL accepted: %s",u)
  }
 }
 input:=trustRequest()
 srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  json.NewEncoder(w).Encode(map[string]any{
   "schema":"aftergraph.works-dispatch-revalidation/1",
   "allowed":true,"actionId":input.ActionID,
   "authorityLeaseId":"auth_"+strings.Repeat("f",32),
   "effectDigest":input.EffectDigest,"canDispatchEffect":false,
  })
 }))
 defer srv.Close()
 c,err:=api.NewTrustGatewayDispatchV2Authority(srv.URL,strings.Repeat("x",32))
 if err!=nil{t.Fatal(err)}
 if err:=c.RevalidateDispatchV2(context.Background(),input);err==nil{
  t.Fatal("wrong Trust authority lease accepted")
 }
 input.ActionID="fake"
 if err:=c.RevalidateDispatchV2(context.Background(),input);err==nil{
  t.Fatal("invalid action ID accepted")
 }
}

func TestTrustGatewayDispatchRejectsMissingAuthorityFlagsAndRedirects(t *testing.T){
 token:=strings.Repeat("z",32)
 in:=trustRequest()
 for _,body:=range []string{
  "{\"schema\":\"aftergraph.works-dispatch-revalidation/1\",\"allowed\":true,\"actionId\":\""+in.ActionID+"\",\"authorityLeaseId\":\""+in.AuthorityLeaseID+"\",\"effectDigest\":\""+in.EffectDigest+"\"}",
  "{\"schema\":\"aftergraph.works-dispatch-revalidation/1\",\"allowed\":true,\"actionId\":\""+in.ActionID+"\",\"authorityLeaseId\":\""+in.AuthorityLeaseID+"\",\"effectDigest\":\""+in.EffectDigest+"\",\"canDispatchEffect\":false,\"authorityGranted\":true}",
 }{
  s:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
   w.Header().Set("Content-Type","application/json")
   _,_ = w.Write([]byte(body))
  }))
  a,err:=api.NewTrustGatewayDispatchV2Authority(s.URL,token)
  if err!=nil{t.Fatal(err)}
  if err=a.RevalidateDispatchV2(context.Background(),in);err==nil{
   t.Fatal("malformed Trust response was accepted")
  }
  s.Close()
 }
 target:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  w.Header().Set("Content-Type","application/json")
  _,_ = w.Write([]byte("{\"schema\":\"aftergraph.works-dispatch-revalidation/1\",\"allowed\":true}"))
 }))
 defer target.Close()
 redirect:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  http.Redirect(w,r,target.URL,http.StatusTemporaryRedirect)
 }))
 defer redirect.Close()
 a,err:=api.NewTrustGatewayDispatchV2Authority(redirect.URL,token)
 if err!=nil{t.Fatal(err)}
 if err=a.RevalidateDispatchV2(context.Background(),in);err==nil{
  t.Fatal("redirected authority response was accepted")
 }
}
