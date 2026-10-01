package store

import (
	"context"
	"testing"
	"time"
)

func TestEconomicTrustRootHistoryGenesisReplayPolicyAndAuthorizedRotation(t *testing.T) {
	st,_:=openBrainStore(t)
	ctx:=context.Background()
	first:=trustRootFixture()
	if _,err:=st.AdvanceEconomicSourceTrustRoot(ctx,first);err!=nil{t.Fatal(err)}

	events,err:=st.ListEconomicSourceTrustRootEvents(ctx,"registry","registry_1")
	if err!=nil{t.Fatal(err)}
	if len(events)!=1 || events[0].Decision!="ADVANCED" || events[0].Sequence!=1 { t.Fatalf("events=%+v",events) }
	if events[0].PreviousEventHash!=economicTrustRootGenesisHash { t.Fatalf("genesis previous=%s",events[0].PreviousEventHash) }

	if _,err:=st.AdvanceEconomicSourceTrustRoot(ctx,first);err!=nil{t.Fatal(err)}
	events,err=st.ListEconomicSourceTrustRootEvents(ctx,"registry","registry_1")
	if err!=nil{t.Fatal(err)}
	if len(events)!=1 { t.Fatalf("replay must not append, events=%+v",events) }

	policy:=first
	policy.Generation=2
	policy.MinAttestationGeneration=2
	policy.ValidFromUnix=2_000
	policy.ValidUntilUnix=20_000
	if _,err:=st.AdvanceEconomicSourceTrustRoot(ctx,policy);err!=nil{t.Fatal(err)}

	rotation:=policy
	rotation.Generation=3
	rotation.TrustRootID="root_registry_2"
	rotation.PublicKeyFingerprint=rootFpB
	rotation.MinAttestationGeneration=5
	rotation.ValidFromUnix=3_000
	rotation.ValidUntilUnix=30_000
	auth:=trustRootRotationAuth(policy,rotation)
	if _,err:=st.AdvanceEconomicSourceTrustRootAuthorized(ctx,rotation,auth);err!=nil{t.Fatal(err)}

	events,err=st.ListEconomicSourceTrustRootEvents(ctx,"registry","registry_1")
	if err!=nil{t.Fatal(err)}
	if len(events)!=3 { t.Fatalf("events=%+v",events) }
	if events[1].Decision!="ADVANCED_POLICY" || events[1].AuthorizationDigest!="" { t.Fatalf("policy=%+v",events[1]) }
	if events[2].Decision!="ROTATED" || events[2].AuthorizationDigest!=auth.AuthorizationDigest { t.Fatalf("rotation=%+v",events[2]) }
	if events[1].PreviousEventHash!=events[0].EventHash || events[2].PreviousEventHash!=events[1].EventHash { t.Fatal("history chain broken") }
}

func TestEconomicTrustRootHistoryDetectsTampering(t *testing.T) {
	st,_:=openBrainStore(t);ctx:=context.Background()
	if _,err:=st.AdvanceEconomicSourceTrustRoot(ctx,trustRootFixture());err!=nil{t.Fatal(err)}
	if _,err:=st.db.Exec("UPDATE economic_source_trust_root_events SET event_hash=? WHERE source_class=? AND source_id=? AND sequence=1",
		"sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff","registry","registry_1");err!=nil{t.Fatal(err)}
	if _,err:=st.ListEconomicSourceTrustRootEvents(ctx,"registry","registry_1");err==nil{t.Fatal("expected history tamper detection")}
}

func TestEconomicTrustRootHistoryRotationCannotAppendWithoutAuthorization(t *testing.T) {
	st,_:=openBrainStore(t);ctx:=context.Background()
	prior:=trustRootFixture()
	if _,err:=st.AdvanceEconomicSourceTrustRoot(ctx,prior);err!=nil{t.Fatal(err)}
	next:=prior
	next.Generation=2
	next.TrustRootID="root_registry_2"
	next.PublicKeyFingerprint=rootFpB
	next.ValidFromUnix=2_000
	next.ValidUntilUnix=20_000
	if _,err:=st.AdvanceEconomicSourceTrustRoot(ctx,next);err==nil{t.Fatal("rotation without authorization must fail")}
	events,err:=st.ListEconomicSourceTrustRootEvents(ctx,"registry","registry_1")
	if err!=nil{t.Fatal(err)}
	if len(events)!=1{t.Fatalf("failed rotation must not append event: %+v",events)}
}

func TestEconomicTrustRootHistoryAuthorizationExpiryStillFailsAtomic(t *testing.T) {
	st,_:=openBrainStore(t);ctx:=context.Background()
	prior:=trustRootFixture()
	if _,err:=st.AdvanceEconomicSourceTrustRoot(ctx,prior);err!=nil{t.Fatal(err)}
	next:=prior
	next.Generation=2
	next.TrustRootID="root_registry_2"
	next.PublicKeyFingerprint=rootFpB
	next.ValidFromUnix=2_000
	next.ValidUntilUnix=20_000
	auth:=trustRootRotationAuth(prior,next)
	auth.Payload.ExpiresAtMillis=time.Now().Add(-time.Second).UnixMilli()
	auth.AuthorizationDigest=economicTrustRootAuthorizationDigest(auth.Payload)
	if _,err:=st.AdvanceEconomicSourceTrustRootAuthorized(ctx,next,auth);err==nil{t.Fatal("expired auth must fail")}
	events,err:=st.ListEconomicSourceTrustRootEvents(ctx,"registry","registry_1")
	if err!=nil{t.Fatal(err)}
	if len(events)!=1{t.Fatalf("failed auth must not append event: %+v",events)}
}
