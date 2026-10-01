package promotion

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JonasAbde/works-execution/packages/workspace"
)

type fakeEvidenceVerifier struct {
	err   error
	calls int
	id    string
	work  string
}
func (f *fakeEvidenceVerifier) Verify(_ context.Context, bundleID, workID string) error {
	f.calls++
	f.id, f.work = bundleID, workID
	return f.err
}

type fakeDecisionVerifier struct {
	err     error
	calls   int
	subject DecisionSubject
}
func (f *fakeDecisionVerifier) VerifyPromotionDecision(_ context.Context, subject DecisionSubject) error {
	f.calls++
	f.subject = subject
	return f.err
}

type fakeBackend struct {
	calls int
	req   AuthorizedRequest
	out   Proposal
	err   error
}
func (f *fakeBackend) ID() string { return "fake" }
func (f *fakeBackend) Propose(_ context.Context, req AuthorizedRequest) (Proposal, error) {
	f.calls++
	f.req = req
	if f.err != nil { return Proposal{}, f.err }
	if f.out.ID == "" {
		f.out = Proposal{
			ID: "prp_test",
			Org: req.Org, WorkID: req.WorkID,
			CandidateSHA: req.Candidate.SHA,
			Target: req.Target,
			StagingRef: "refs/heads/works/promotion/test",
			PullRequestURL: "https://github.com/Aftergraph/runtime/pull/1",
			PullRequestNumber: 1,
			EvidenceBundleID: req.EvidenceBundleID,
			DecisionRef: req.DecisionRef,
			PolicyDecisionID: req.PolicyDecisionID,
			CreatedAt: time.Date(2026,10,1,18,0,0,0,time.UTC),
		}
	}
	return f.out,nil
}
func (f *fakeBackend) Get(_ context.Context, p Proposal) (Proposal,error) { return p,nil }

func validRequest() Request {
	return Request{
		IdempotencyKey:"promote-1",
		Org:"aftergraph",
		WorkID:"wrk_0123456789abcdef0123456789abcdef",
		Workspace: workspace.Workspace{
			ID:"wsp_1", ProviderID:"github", Org:"aftergraph",
			WorkID:"wrk_0123456789abcdef0123456789abcdef",
			Name:"Aftergraph/runtime",
			RemoteURL:"https://github.com/Aftergraph/runtime.git",
			DefaultBranch:"works/agent",
			Baseline: workspace.SourceRef{
				Provider:"github", Repository:"Aftergraph/runtime",
				Ref:"refs/heads/main",
				SHA:"0123456789012345678901234567890123456789",
			},
			Mode: workspace.ModeWrite,
			CredentialRef: mustSecretRef("secret://workspace/token_1"),
			CreatedAt: time.Date(2026,10,1,17,0,0,0,time.UTC),
		},
		Candidate: workspace.Candidate{
			WorkspaceID:"wsp_1", Repository:"Aftergraph/runtime",
			Ref:"refs/heads/works/agent",
			SHA:"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ProducedAt:time.Date(2026,10,1,17,30,0,0,time.UTC),
		},
		EvidenceBundleID:"evb_0123456789abcdef0123456789abcdef",
		DecisionRef:"/org/deadbeef/decisions/promote-1",
		PolicyDecisionID:"pdr_99999999999999999999999999999999",
		Target:Target{Provider:"github",Repository:"Aftergraph/runtime",Branch:"main"},
	}
}

func TestRequestValidateAcceptsBoundCandidate(t *testing.T) {
	if err:=validRequest().Validate(); err!=nil { t.Fatalf("valid request rejected: %v",err) }
}

func TestRequestValidateRejectsForeignWorkspace(t *testing.T) {
	r:=validRequest(); r.Org="other"
	if err:=r.Validate(); !errors.Is(err,ErrForeignWorkspace) { t.Fatalf("got %v want ErrForeignWorkspace",err) }
}

func TestRequestValidateRejectsCandidateWorkspaceMismatch(t *testing.T) {
	r:=validRequest(); r.Candidate.WorkspaceID="wsp_other"
	if err:=r.Validate(); !errors.Is(err,ErrCandidateMismatch) { t.Fatalf("got %v want ErrCandidateMismatch",err) }
}

func TestRequestValidateRequiresImmutableGitCandidate(t *testing.T) {
	r:=validRequest(); r.Candidate.SHA="branch-head"
	if err:=r.Validate(); !errors.Is(err,ErrImmutableCandidateRequired) { t.Fatalf("got %v want ErrImmutableCandidateRequired",err) }
}

func TestRequestValidateRequiresEvidenceDecisionAndTarget(t *testing.T) {
	cases:=[]struct{name string; mutate func(*Request)}{
		{"evidence",func(r *Request){r.EvidenceBundleID=""}},
		{"decision",func(r *Request){r.DecisionRef=""}},
		{"target-provider",func(r *Request){r.Target.Provider=""}},
		{"target-repository",func(r *Request){r.Target.Repository=""}},
		{"target-branch",func(r *Request){r.Target.Branch=""}},
	}
	for _,tc:=range cases{
		t.Run(tc.name,func(t *testing.T){
			r:=validRequest(); tc.mutate(&r)
			if err:=r.Validate(); !errors.Is(err,ErrMalformed){ t.Fatalf("got %v want ErrMalformed",err) }
		})
	}
}

func TestServiceEvidenceFailureNeverCallsDecisionOrBackend(t *testing.T) {
	ev:=&fakeEvidenceVerifier{err:errors.New("bad evidence")}
	dv:=&fakeDecisionVerifier{}
	be:=&fakeBackend{}
	s,err:=NewService(ev,dv,be); if err!=nil{t.Fatal(err)}
	if _,err=s.Propose(context.Background(),validRequest()); !errors.Is(err,ErrEvidenceNotVerified){
		t.Fatalf("got %v want ErrEvidenceNotVerified",err)
	}
	if ev.calls!=1 || dv.calls!=0 || be.calls!=0 {
		t.Fatalf("calls evidence=%d decision=%d backend=%d",ev.calls,dv.calls,be.calls)
	}
}

func TestServiceDecisionFailureNeverCallsBackend(t *testing.T) {
	ev:=&fakeEvidenceVerifier{}
	dv:=&fakeDecisionVerifier{err:errors.New("no authority")}
	be:=&fakeBackend{}
	s,err:=NewService(ev,dv,be); if err!=nil{t.Fatal(err)}
	if _,err=s.Propose(context.Background(),validRequest()); !errors.Is(err,ErrDecisionNotAuthorized){
		t.Fatalf("got %v want ErrDecisionNotAuthorized",err)
	}
	if ev.calls!=1 || dv.calls!=1 || be.calls!=0 {
		t.Fatalf("calls evidence=%d decision=%d backend=%d",ev.calls,dv.calls,be.calls)
	}
}

func TestServiceBindsExactDecisionSubjectBeforeBackend(t *testing.T) {
	ev:=&fakeEvidenceVerifier{}
	dv:=&fakeDecisionVerifier{}
	be:=&fakeBackend{}
	s,err:=NewService(ev,dv,be); if err!=nil{t.Fatal(err)}
	req:=validRequest()
	got,err:=s.Propose(context.Background(),req); if err!=nil{t.Fatal(err)}
	want:=DecisionSubject{
		Org:req.Org, WorkID:req.WorkID, CandidateSHA:req.Candidate.SHA,
		EvidenceBundleID:req.EvidenceBundleID, DecisionRef:req.DecisionRef,
		PolicyDecisionID:req.PolicyDecisionID,
	}
	if dv.subject!=want { t.Fatalf("decision subject=%#v want %#v",dv.subject,want) }
	if be.calls!=1 { t.Fatalf("backend calls=%d want 1",be.calls) }
	if got.CandidateSHA!=req.Candidate.SHA || got.EvidenceBundleID!=req.EvidenceBundleID || got.DecisionRef!=req.DecisionRef {
		t.Fatalf("proposal lost provenance: %#v",got)
	}
}
