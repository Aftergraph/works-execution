package circuitrun

import "testing"

func TestEffectBindingInputValidate(t *testing.T) {
	valid := EffectBindingInput{
		CircuitRunID: "crun_0123456789abcdef0123456789abcdef",
		WorksExecutionID: "wexec/idem-1",
	}
	if err := valid.Validate(); err != nil { t.Fatal(err) }
	cases := []EffectBindingInput{
		{WorksExecutionID: valid.WorksExecutionID},
		{CircuitRunID: valid.CircuitRunID},
		{CircuitRunID: "crun_bad", WorksExecutionID: valid.WorksExecutionID},
	}
	for i, in := range cases {
		if err := in.Validate(); err == nil { t.Fatalf("case %d expected error", i) }
	}
}
