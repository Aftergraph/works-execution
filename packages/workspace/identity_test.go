package workspace

import (
	"testing"
)

func TestWorkspaceIdentitySeparatesKeyFromSpec(t *testing.T) {
	base := validSpec()
	keyA, specA, err := workspaceIdentity(base)
	if err != nil { t.Fatal(err) }
	keyAgain, specAgain, err := workspaceIdentity(base)
	if err != nil { t.Fatal(err) }
	if keyA != keyAgain || specA != specAgain {
		t.Fatal("workspace identity is not deterministic")
	}

	changedSpec := base
	changedSpec.Name = "changed"
	keyB, specB, err := workspaceIdentity(changedSpec)
	if err != nil { t.Fatal(err) }
	if keyB != keyA {
		t.Fatal("same idempotency key changed key prefix")
	}
	if specB == specA {
		t.Fatal("changed spec did not change fingerprint")
	}

	changedKey := base
	changedKey.IdempotencyKey = "different-key"
	keyC, _, err := workspaceIdentity(changedKey)
	if err != nil { t.Fatal(err) }
	if keyC == keyA {
		t.Fatal("different idempotency key reused key prefix")
	}
}
