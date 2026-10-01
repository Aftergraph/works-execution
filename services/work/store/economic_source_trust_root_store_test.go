package store

import (
	"context"
	"errors"
	"testing"
)

const (
	rootFpA="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rootFpB="sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func trustRootFixture() EconomicSourceTrustRootState {
	return EconomicSourceTrustRootState{
		SourceClass:"registry",SourceID:"registry_1",TrustRootID:"root_registry_1",
		PublicKeyFingerprint:rootFpA,Generation:1,MinAttestationGeneration:1,
		Status:"ACTIVE",ValidFromUnix:1_000,ValidUntilUnix:10_000,
	}
}

func TestEconomicTrustRootGenesisReplayAndRotation(t *testing.T) {
	st,_:=openBrainStore(t)
	ctx:=context.Background()
	first,err:=st.AdvanceEconomicSourceTrustRoot(ctx,trustRootFixture())
	if err!=nil { t.Fatal(err) }
	if first.Decision!="ADVANCED" || first.Current.Revision!=1 { t.Fatalf("first=%+v",first) }

	replay,err:=st.AdvanceEconomicSourceTrustRoot(ctx,trustRootFixture())
	if err!=nil { t.Fatal(err) }
	if replay.Decision!="IDEMPOTENT_REPLAY" || replay.Current.Revision!=1 { t.Fatalf("replay=%+v",replay) }

	next:=trustRootFixture()
	next.Generation=2
	next.TrustRootID="root_registry_2"
	next.PublicKeyFingerprint=rootFpB
	next.MinAttestationGeneration=5
	next.ValidFromUnix=2_000
	next.ValidUntilUnix=20_000
	rot,err:=st.AdvanceEconomicSourceTrustRoot(ctx,next)
	if err!=nil { t.Fatal(err) }
	if rot.Decision!="ROTATED" || rot.Current.Revision!=2 { t.Fatalf("rot=%+v",rot) }

	got,err:=st.GetEconomicSourceTrustRoot(ctx,"registry","registry_1")
	if err!=nil { t.Fatal(err) }
	if got.PublicKeyFingerprint!=rootFpB || got.Generation!=2 || got.StateDigest=="" { t.Fatalf("got=%+v",got) }
}

func TestEconomicTrustRootRejectsEquivocationRegressionGapAndFloorRollback(t *testing.T) {
	st,_:=openBrainStore(t); ctx:=context.Background()
	if _,err:=st.AdvanceEconomicSourceTrustRoot(ctx,trustRootFixture());err!=nil{t.Fatal(err)}

	eq:=trustRootFixture();eq.Status="REVOKED"
	if _,err:=st.AdvanceEconomicSourceTrustRoot(ctx,eq);!errors.Is(err,ErrEconomicTrustRootEquivocation){t.Fatalf("equivocation err=%v",err)}

	reg:=trustRootFixture();reg.Generation=0
	if _,err:=st.AdvanceEconomicSourceTrustRoot(ctx,reg);err==nil{t.Fatal("expected invalid generation")}

	gap:=trustRootFixture();gap.Generation=3
	if _,err:=st.AdvanceEconomicSourceTrustRoot(ctx,gap);!errors.Is(err,ErrEconomicTrustRootGenerationGap){t.Fatalf("gap err=%v",err)}

	next:=trustRootFixture();next.Generation=2;next.MinAttestationGeneration=2;next.ValidFromUnix=2_000;next.ValidUntilUnix=20_000
	if _,err:=st.AdvanceEconomicSourceTrustRoot(ctx,next);err!=nil{t.Fatal(err)}
	floor:=next;floor.Generation=3;floor.MinAttestationGeneration=1
	if _,err:=st.AdvanceEconomicSourceTrustRoot(ctx,floor);!errors.Is(err,ErrEconomicTrustRootFloorRegression){t.Fatalf("floor err=%v",err)}
}

func TestEconomicTrustRootRejectsRevokedKeyRevival(t *testing.T) {
	st,_:=openBrainStore(t);ctx:=context.Background()
	first:=trustRootFixture();first.Status="REVOKED"
	if _,err:=st.AdvanceEconomicSourceTrustRoot(ctx,first);err!=nil{t.Fatal(err)}
	next:=first;next.Generation=2;next.Status="ACTIVE";next.ValidFromUnix=2_000;next.ValidUntilUnix=20_000
	if _,err:=st.AdvanceEconomicSourceTrustRoot(ctx,next);!errors.Is(err,ErrEconomicTrustRootRevivalForbidden){t.Fatalf("err=%v",err)}
}

func TestEconomicTrustRootAllowsRecoveryWithNewKeyAfterRevocation(t *testing.T) {
	st,_:=openBrainStore(t);ctx:=context.Background()
	first:=trustRootFixture();first.Status="REVOKED"
	if _,err:=st.AdvanceEconomicSourceTrustRoot(ctx,first);err!=nil{t.Fatal(err)}
	next:=first;next.Generation=2;next.Status="ACTIVE";next.TrustRootID="root_registry_2";next.PublicKeyFingerprint=rootFpB;next.ValidFromUnix=2_000;next.ValidUntilUnix=20_000
	out,err:=st.AdvanceEconomicSourceTrustRoot(ctx,next)
	if err!=nil{t.Fatal(err)}
	if out.Decision!="ROTATED"{t.Fatalf("out=%+v",out)}
}
