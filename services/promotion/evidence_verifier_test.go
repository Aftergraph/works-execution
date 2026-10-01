package promotionservice

import (
	"context"
	"errors"
	"strings"
	"testing"

	domain "github.com/JonasAbde/works-execution/packages/promotion"
	"github.com/JonasAbde/works-execution/services/evidence"
)

type fakeBundleLoader struct { bundle *evidence.Bundle; err error }
func (f fakeBundleLoader) LoadEvidenceBundle(context.Context,string)(*evidence.Bundle,error){ return f.bundle,f.err }

type fakeKeyResolver struct { keyID string; key []byte; err error }
func (f fakeKeyResolver) ResolveEvidenceVerificationKey(context.Context,string)(string,[]byte,error){
	return f.keyID,f.key,f.err
}

func TestEvidenceVerifierRejectsMissingBundle(t *testing.T){
	v:=newEvidenceVerifierWithVerify(fakeBundleLoader{err:errors.New("not found")},fakeKeyResolver{},nil)
	err:=v.Verify(context.Background(),"evb_missing","wrk_1")
	if !errors.Is(err,domain.ErrEvidenceNotVerified){ t.Fatalf("got %v",err) }
}

func TestEvidenceVerifierRejectsBundleAndWorkMismatchBeforeCrypto(t *testing.T){
	calls:=0
	verify:=func(*evidence.Bundle,string,[]byte)(*evidence.BundleVerificationResult,error){
		calls++; return &evidence.BundleVerificationResult{Valid:true},nil
	}
	v:=newEvidenceVerifierWithVerify(fakeBundleLoader{bundle:&evidence.Bundle{BundleID:"evb_other",WorkID:"wrk_other"}},fakeKeyResolver{},verify)
	if err:=v.Verify(context.Background(),"evb_expected","wrk_1"); !errors.Is(err,domain.ErrEvidenceNotVerified){
		t.Fatalf("got %v",err)
	}
	if calls!=0 { t.Fatalf("crypto verifier called on identity mismatch: %d",calls) }
}

func TestEvidenceVerifierRejectsCryptographicFailureWithoutLeakingKey(t *testing.T){
	secret:=[]byte("super-secret-verification-key")
	v:=newEvidenceVerifierWithVerify(
		fakeBundleLoader{bundle:&evidence.Bundle{BundleID:"evb_1",WorkID:"wrk_1"}},
		fakeKeyResolver{keyID:"kid",key:secret},
		func(*evidence.Bundle,string,[]byte)(*evidence.BundleVerificationResult,error){
			return &evidence.BundleVerificationResult{Valid:false,Errors:[]string{"signature invalid"}},nil
		},
	)
	err:=v.Verify(context.Background(),"evb_1","wrk_1")
	if !errors.Is(err,domain.ErrEvidenceNotVerified){ t.Fatalf("got %v",err) }
	if strings.Contains(err.Error(),string(secret)){ t.Fatal("verification key leaked in error") }
}

func TestEvidenceVerifierAcceptsFullyVerifiedExactBundle(t *testing.T){
	v:=newEvidenceVerifierWithVerify(
		fakeBundleLoader{bundle:&evidence.Bundle{BundleID:"evb_1",WorkID:"wrk_1"}},
		fakeKeyResolver{keyID:"kid",key:[]byte("key")},
		func(b *evidence.Bundle,keyID string,key []byte)(*evidence.BundleVerificationResult,error){
			if b.BundleID!="evb_1" || keyID!="kid" || string(key)!="key"{ t.Fatal("wrong verification inputs") }
			return &evidence.BundleVerificationResult{
				Valid:true,SignatureValid:true,ContentHashValid:true,
				IntegrityValid:true,CorrelationComplete:true,
			},nil
		},
	)
	if err:=v.Verify(context.Background(),"evb_1","wrk_1"); err!=nil{ t.Fatal(err) }
}

func TestNewEvidenceVerifierUsesRealEvidenceVerifier(t *testing.T){
	v,err:=NewEvidenceVerifier(
		fakeBundleLoader{bundle:&evidence.Bundle{BundleID:"evb_invalid",WorkID:"wrk_1"}},
		fakeKeyResolver{keyID:"kid",key:[]byte("key")},
	)
	if err!=nil{t.Fatal(err)}
	if err:=v.Verify(context.Background(),"evb_invalid","wrk_1"); !errors.Is(err,domain.ErrEvidenceNotVerified){
		t.Fatalf("invalid real bundle unexpectedly passed: %v",err)
	}
}
