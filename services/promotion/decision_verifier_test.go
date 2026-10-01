package promotionservice

import (
	"context"
	"errors"
	"testing"

	domain "github.com/JonasAbde/works-execution/packages/promotion"
)

type fakeDecisionLoader struct { record *DecisionRecord; err error }
func (f fakeDecisionLoader) LoadPromotionDecision(context.Context,string)(*DecisionRecord,error){ return f.record,f.err }

func validDecision() *DecisionRecord {
	return &DecisionRecord{
		Ref:"/org/deadbeef/decisions/promote-1",
		Org:"aftergraph",WorkID:"wrk_1",
		CandidateSHA:"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		EvidenceBundleID:"evb_1",PolicyDecisionID:"pdr_1",
		Authoritative:true,Promotion:"human_stamped",HumanStamp:"human-1",
	}
}
func validSubject() domain.DecisionSubject {
	return domain.DecisionSubject{
		Org:"aftergraph",WorkID:"wrk_1",
		CandidateSHA:"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		EvidenceBundleID:"evb_1",DecisionRef:"/org/deadbeef/decisions/promote-1",
		PolicyDecisionID:"pdr_1",
	}
}

func TestDecisionVerifierRejectsMissingDecision(t *testing.T){
	v,err:=NewDecisionVerifier(fakeDecisionLoader{err:errors.New("not found")}); if err!=nil{t.Fatal(err)}
	if err:=v.VerifyPromotionDecision(context.Background(),validSubject()); !errors.Is(err,domain.ErrDecisionNotAuthorized){
		t.Fatalf("got %v",err)
	}
}

func TestDecisionVerifierRequiresHumanStampedAuthority(t *testing.T){
	cases:=[]struct{name string; mutate func(*DecisionRecord)}{
		{"not-authoritative",func(r *DecisionRecord){r.Authoritative=false}},
		{"wrong-promotion",func(r *DecisionRecord){r.Promotion="none"}},
		{"empty-stamp",func(r *DecisionRecord){r.HumanStamp=""}},
		{"tombstone",func(r *DecisionRecord){r.Tombstone=true}},
	}
	for _,tc:=range cases{
		t.Run(tc.name,func(t *testing.T){
			r:=validDecision(); tc.mutate(r)
			v,_:=NewDecisionVerifier(fakeDecisionLoader{record:r})
			if err:=v.VerifyPromotionDecision(context.Background(),validSubject()); !errors.Is(err,domain.ErrDecisionNotAuthorized){
				t.Fatalf("got %v",err)
			}
		})
	}
}

func TestDecisionVerifierRejectsBindingMismatch(t *testing.T){
	cases:=[]struct{name string; mutate func(*DecisionRecord)}{
		{"ref",func(r *DecisionRecord){r.Ref="/org/deadbeef/decisions/other"}},
		{"org",func(r *DecisionRecord){r.Org="other"}},
		{"work",func(r *DecisionRecord){r.WorkID="other"}},
		{"candidate",func(r *DecisionRecord){r.CandidateSHA="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},
		{"evidence",func(r *DecisionRecord){r.EvidenceBundleID="evb_other"}},
		{"policy",func(r *DecisionRecord){r.PolicyDecisionID="pdr_other"}},
	}
	for _,tc:=range cases{
		t.Run(tc.name,func(t *testing.T){
			r:=validDecision(); tc.mutate(r)
			v,_:=NewDecisionVerifier(fakeDecisionLoader{record:r})
			if err:=v.VerifyPromotionDecision(context.Background(),validSubject()); !errors.Is(err,domain.ErrDecisionNotAuthorized){
				t.Fatalf("got %v",err)
			}
		})
	}
}

func TestDecisionVerifierAcceptsExactAuthoritativeBinding(t *testing.T){
	v,err:=NewDecisionVerifier(fakeDecisionLoader{record:validDecision()}); if err!=nil{t.Fatal(err)}
	if err:=v.VerifyPromotionDecision(context.Background(),validSubject()); err!=nil{t.Fatal(err)}
}
