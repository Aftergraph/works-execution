package promotion

import (
	"context"
	"errors"
	"testing"

	"github.com/JonasAbde/works-execution/packages/workspace"
)

type recordingMaterializer struct {
	calls int
	got AuthorizedRequest
	out MaterializedCandidate
	err error
}
func (m *recordingMaterializer) Materialize(_ context.Context, req AuthorizedRequest) (MaterializedCandidate,error) {
	m.calls++
	m.got=req
	return m.out,m.err
}

type recordingBackend struct {
	calls int
	got AuthorizedRequest
	proposal Proposal
}
func (b *recordingBackend) ID() string { return "recording" }
func (b *recordingBackend) Propose(_ context.Context, req AuthorizedRequest)(Proposal,error){
	b.calls++; b.got=req; return b.proposal,nil
}
func (b *recordingBackend) Get(_ context.Context,p Proposal)(Proposal,error){return p,nil}

func externalRequest() Request {
	r:=validRequest()
	r.Workspace.ProviderID=workspace.CloudflareArtifactsProviderID
	r.Workspace.Name="wsp-external"
	r.Workspace.RemoteURL="https://acct.artifacts.cloudflare.net/git/default/wsp-external.git"
	r.Workspace.ID="wsp_external"
	r.Candidate.WorkspaceID=r.Workspace.ID
	r.Candidate.Repository=r.Workspace.Name
	r.Target.Repository="Aftergraph/runtime"
	return r
}

func authorizedForMaterialization(t *testing.T,r Request) AuthorizedRequest {
	t.Helper()
	key,fp,err:=promotionIdentity(r)
	if err!=nil{t.Fatal(err)}
	return AuthorizedRequest{request:r,keyHash:key,fp:fp}
}

func TestMaterializingBackendPassesNativeTargetCandidateThrough(t *testing.T){
	r:=validRequest()
	auth:=authorizedForMaterialization(t,r)
	inner:=&recordingBackend{proposal:Proposal{ID:"prp_native"}}
	mat:=&recordingMaterializer{}
	b,err:=NewMaterializingBackend(inner,mat)
	if err!=nil{t.Fatal(err)}
	got,err:=b.Propose(context.Background(),auth)
	if err!=nil{t.Fatal(err)}
	if got.ID!="prp_native" || inner.calls!=1 || mat.calls!=0{
		t.Fatalf("unexpected pass-through: proposal=%#v inner=%d materializer=%d",got,inner.calls,mat.calls)
	}
}

func TestMaterializingBackendImportsExternalCandidateAfterAuthority(t *testing.T){
	r:=externalRequest()
	auth:=authorizedForMaterialization(t,r)
	inner:=&recordingBackend{proposal:Proposal{ID:"prp_external"}}
	mat:=&recordingMaterializer{out:MaterializedCandidate{
		Repository:r.Target.Repository,
		Ref:"refs/heads/works/materialized/test",
		SHA:r.Candidate.SHA,
		SourceRepository:r.Candidate.Repository,
		SourceRef:r.Candidate.Ref,
		SourceWorkspaceID:r.Workspace.ID,
	}}
	b,err:=NewMaterializingBackend(inner,mat)
	if err!=nil{t.Fatal(err)}
	got,err:=b.Propose(context.Background(),auth)
	if err!=nil{t.Fatal(err)}
	if got.ID!="prp_external" || mat.calls!=1 || inner.calls!=1{t.Fatalf("bad calls")}
	innerReq:=inner.got.Request()
	if innerReq.Candidate.Repository!=r.Target.Repository || innerReq.Candidate.SHA!=r.Candidate.SHA {
		t.Fatalf("backend did not receive canonical candidate: %#v",innerReq.Candidate)
	}
	if inner.got.KeyHash()!=auth.KeyHash() || inner.got.Fingerprint()!=auth.Fingerprint(){
		t.Fatal("materialization changed promotion idempotency identity")
	}
	if mat.got.Request().Candidate.Repository!=r.Candidate.Repository{
		t.Fatal("materializer lost original source coordinate")
	}
}

func TestMaterializingBackendFailsClosedOnLineageDrift(t *testing.T){
	r:=externalRequest()
	auth:=authorizedForMaterialization(t,r)
	cases:=[]struct{name string; out MaterializedCandidate}{
		{"sha drift",MaterializedCandidate{Repository:r.Target.Repository,SHA:"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",SourceRepository:r.Candidate.Repository,SourceWorkspaceID:r.Workspace.ID}},
		{"target drift",MaterializedCandidate{Repository:"Aftergraph/other",SHA:r.Candidate.SHA,SourceRepository:r.Candidate.Repository,SourceWorkspaceID:r.Workspace.ID}},
		{"workspace drift",MaterializedCandidate{Repository:r.Target.Repository,SHA:r.Candidate.SHA,SourceRepository:r.Candidate.Repository,SourceWorkspaceID:"wsp_other"}},
		{"source drift",MaterializedCandidate{Repository:r.Target.Repository,SHA:r.Candidate.SHA,SourceRepository:"other",SourceWorkspaceID:r.Workspace.ID}},
	}
	for _,tc:=range cases{
		t.Run(tc.name,func(t *testing.T){
			inner:=&recordingBackend{}
			mat:=&recordingMaterializer{out:tc.out}
			b,_:=NewMaterializingBackend(inner,mat)
			_,err:=b.Propose(context.Background(),auth)
			if !errors.Is(err,ErrMaterializationMismatch){t.Fatalf("got %v",err)}
			if inner.calls!=0{t.Fatal("canonical backend called after materialization lineage drift")}
		})
	}
}

func TestMaterializingBackendMaterializerFailureNeverCallsCanonicalBackend(t *testing.T){
	r:=externalRequest()
	auth:=authorizedForMaterialization(t,r)
	inner:=&recordingBackend{}
	mat:=&recordingMaterializer{err:errors.New("fetch failed")}
	b,_:=NewMaterializingBackend(inner,mat)
	_,err:=b.Propose(context.Background(),auth)
	if !errors.Is(err,ErrMaterializationFailed){t.Fatalf("got %v",err)}
	if inner.calls!=0{t.Fatal("canonical backend called after materializer failure")}
}

func TestMaterializedCandidateCarriesNoCredentialMaterial(t *testing.T){
	m:=MaterializedCandidate{
		Repository:"Aftergraph/runtime",Ref:"refs/heads/works/materialized/test",
		SHA:"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SourceRepository:"wsp-external",SourceRef:"refs/heads/main",SourceWorkspaceID:"wsp_1",
	}
	if err:=m.Validate();err!=nil{t.Fatal(err)}
}
