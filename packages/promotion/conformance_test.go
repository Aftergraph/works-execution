package promotion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type referenceBackend struct {
	byKey map[string]struct{
		fp string
		p Proposal
	}
}

func newReferenceBackend() *referenceBackend {
	return &referenceBackend{byKey:make(map[string]struct{fp string; p Proposal})}
}
func (b *referenceBackend) ID() string { return "reference" }
func (b *referenceBackend) Propose(_ context.Context, a AuthorizedRequest) (Proposal,error) {
	if got,ok:=b.byKey[a.KeyHash()]; ok {
		if got.fp!=a.Fingerprint() { return Proposal{},ErrIdempotencyConflict }
		return got.p,nil
	}
	r:=a.Request()
	p:=Proposal{
		ID:"prp_"+a.KeyHash(),Org:r.Org,WorkID:r.WorkID,CandidateSHA:r.Candidate.SHA,
		Target:r.Target,StagingRef:"refs/heads/works/promotion/"+a.KeyHash()+"-"+a.Fingerprint(),
		PullRequestURL:"https://github.com/"+r.Target.Repository+"/pull/1",PullRequestNumber:1,
		EvidenceBundleID:r.EvidenceBundleID,DecisionRef:r.DecisionRef,PolicyDecisionID:r.PolicyDecisionID,
		CreatedAt:r.Candidate.ProducedAt,
	}
	b.byKey[a.KeyHash()]=struct{fp string; p Proposal}{a.Fingerprint(),p}
	return p,nil
}
func (b *referenceBackend) Get(_ context.Context,p Proposal)(Proposal,error){
	for _,got:=range b.byKey { if got.p.ID==p.ID{return got.p,nil} }
	return Proposal{},ErrNotFound
}

func ConformanceSuite(t *testing.T, backend Backend) {
	t.Helper()
	ev:=&fakeEvidenceVerifier{}
	dv:=&fakeDecisionVerifier{}
	s,err:=NewService(ev,dv,backend); if err!=nil{t.Fatal(err)}
	ctx:=context.Background()

	t.Run("valid-proposal-preserves-provenance",func(t *testing.T){
		r:=validRequest()
		p,err:=s.Propose(ctx,r); if err!=nil{t.Fatal(err)}
		if p.Org!=r.Org || p.WorkID!=r.WorkID || p.WorkspaceID!=r.Workspace.ID ||
			p.CandidateSHA!=r.Candidate.SHA || p.EvidenceBundleID!=r.EvidenceBundleID ||
			p.DecisionRef!=r.DecisionRef || p.Target!=r.Target {
			t.Fatalf("proposal lost provenance: %#v",p)
		}
	})

	t.Run("exact-replay-reuses-proposal",func(t *testing.T){
		r:=validRequest()
		a,err:=s.Propose(ctx,r); if err!=nil{t.Fatal(err)}
		b,err:=s.Propose(ctx,r); if err!=nil{t.Fatal(err)}
		if a.ID!=b.ID || a.PullRequestNumber!=b.PullRequestNumber {
			t.Fatalf("replay changed proposal: %#v %#v",a,b)
		}
	})

	t.Run("same-key-changed-request-conflicts",func(t *testing.T){
		r:=validRequest()
		if _,err:=s.Propose(ctx,r); err!=nil{t.Fatal(err)}
		r.DecisionRef="/org/deadbeef/decisions/other"
		if _,err:=s.Propose(ctx,r); !errors.Is(err,ErrIdempotencyConflict){
			t.Fatalf("got %v want ErrIdempotencyConflict",err)
		}
	})

	t.Run("foreign-work-fails-before-backend",func(t *testing.T){
		r:=validRequest(); r.WorkID="wrk_other"
		if _,err:=s.Propose(ctx,r); !errors.Is(err,ErrForeignWorkspace){
			t.Fatalf("got %v want ErrForeignWorkspace",err)
		}
	})

	t.Run("proposal-wire-has-no-later-authority-state",func(t *testing.T){
		p,err:=s.Propose(ctx,validRequest()); if err!=nil{t.Fatal(err)}
		raw,err:=json.Marshal(p); if err!=nil{t.Fatal(err)}
		wire:=string(raw)
		for _,forbidden:=range []string{"merged","released","auto_merge","approved","credential_ref","raw_token"} {
			if strings.Contains(wire,forbidden) { t.Fatalf("proposal wire contains forbidden authority/secret field %q: %s",forbidden,wire) }
		}
	})
}

func TestPromotionConformanceReferenceBackend(t *testing.T){
	ConformanceSuite(t,newReferenceBackend())
}
