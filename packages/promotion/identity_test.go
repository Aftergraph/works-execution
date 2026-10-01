package promotion

import "testing"

func TestPromotionIdentitySeparatesRetryKeyFromCanonicalRequest(t *testing.T) {
	base:=validRequest()
	keyA,fpA,err:=promotionIdentity(base); if err!=nil{t.Fatal(err)}
	keyAgain,fpAgain,err:=promotionIdentity(base); if err!=nil{t.Fatal(err)}
	if keyA!=keyAgain || fpA!=fpAgain { t.Fatal("identity is not deterministic") }

	changed:=base
	changed.DecisionRef="/org/deadbeef/decisions/promote-2"
	keyB,fpB,err:=promotionIdentity(changed); if err!=nil{t.Fatal(err)}
	if keyB!=keyA { t.Fatal("same idempotency key changed key hash") }
	if fpB==fpA { t.Fatal("changed canonical request did not change fingerprint") }

	changedKey:=base
	changedKey.IdempotencyKey="promote-2"
	keyC,_,err:=promotionIdentity(changedKey); if err!=nil{t.Fatal(err)}
	if keyC==keyA { t.Fatal("different idempotency key reused key hash") }
}
