package webhook

import "testing"

// Regression: GitHub delivers branch deletions as push events with
// after=0000...0000 (a literal 40-zero string). ShouldCreateWork used
// to accept them (SHA != "" is true), producing FAILED works with
// checkout errors and publisher 422 noise against the null SHA.
func TestShouldCreateWork_PushNullSHA_BranchDeletion(t *testing.T) {
	d := Delivery{
		Event:        EventPush,
		Ref:          "refs/heads/works-ci-cutover",
		SHA:          "0000000000000000000000000000000000000000",
		RepoFullName: "JonasAbde/aie",
	}
	if d.ShouldCreateWork() {
		t.Error("expected ShouldCreateWork()=false for branch-deletion push (null SHA)")
	}
}

// The empty-string guard must keep working.
func TestShouldCreateWork_PushEmptySHA(t *testing.T) {
	d := Delivery{
		Event:        EventPush,
		Ref:          "refs/heads/main",
		SHA:          "",
		RepoFullName: "JonasAbde/aie",
	}
	if d.ShouldCreateWork() {
		t.Error("expected ShouldCreateWork()=false for empty SHA")
	}
}

// Normal pushes must still create works (guard against over-filtering).
func TestShouldCreateWork_PushRealSHA(t *testing.T) {
	d := Delivery{
		Event:        EventPush,
		Ref:          "refs/heads/main",
		SHA:          "a2c70c5940c9bdc11f1083e3ce63b554ef8d6ae2",
		RepoFullName: "JonasAbde/aie",
	}
	if !d.ShouldCreateWork() {
		t.Error("expected ShouldCreateWork()=true for normal push")
	}
}
