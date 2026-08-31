package main

import (
	"testing"
)

func TestSplitOwnerrepo(t *testing.T) {
	cases := []struct {
		in   string
		want [2]string
	}{
		{"JonasAbde/works-execution", [2]string{"JonasAbde", "works-execution"}},
		{"org/team/project", [2]string{"org", "team/project"}},
		{"solo", [2]string{"solo", ""}},
		{"", [2]string{"", ""}},
	}
	for _, c := range cases {
		owner, name := splitOwnerrepo(c.in)
		if [2]string{owner, name} != c.want {
			t.Errorf("splitOwnerrepo(%q) = (%q,%q), want %v", c.in, owner, name, c.want)
		}
	}
}

func TestMinMax(t *testing.T) {
	if min(3,5) != 3 || min(5,3) != 3 {
		t.Error("min wrong")
	}
	if max(3,5) != 5 || max(5,3) != 5 {
		t.Error("max wrong")
	}
}