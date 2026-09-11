package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/JonasAbde/works-execution/packages/goldenmission"
)

type goldenMissionCLIInput struct {
	RunnerPrincipal string                      `json:"runner_principal"`
	Mission         goldenmission.Mission       `json:"mission"`
	Verification    *goldenmission.Verification `json:"verification,omitempty"`
}

type goldenMissionCLIOutput struct {
	goldenmission.Result
	EffectCount int `json:"effect_count"`
}

func runGoldenMissionInput(r io.Reader, w io.Writer) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()

	var input goldenMissionCLIInput
	if err := dec.Decode(&input); err != nil {
		return fmt.Errorf("decode golden mission input: %w", err)
	}

	effects := goldenmission.NewEffectLog()
	runner, err := goldenmission.NewRunner(input.RunnerPrincipal, effects)
	if err != nil {
		return err
	}

	var result goldenmission.Result
	if input.Verification != nil {
		result, err = runner.RunWithVerification(input.Mission, *input.Verification)
	} else {
		result, err = runner.Run(input.Mission)
	}
	if err != nil {
		return err
	}

	output := goldenMissionCLIOutput{
		Result:      result,
		EffectCount: effects.Count(input.Mission.Canonical.WorkID + "|" + input.Mission.Canonical.ActionID),
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(output); err != nil {
		return fmt.Errorf("encode golden mission output: %w", err)
	}
	return nil
}

func goldenMissionCmd(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("golden-mission", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	inputPath := fs.String("input", "-", "JSON fixture path, or - for stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("golden-mission: unexpected positional arguments")
	}

	reader := stdin
	if *inputPath != "-" {
		f, err := os.Open(*inputPath)
		if err != nil {
			return fmt.Errorf("open golden mission input: %w", err)
		}
		defer f.Close()
		reader = f
	}
	return runGoldenMissionInput(reader, stdout)
}
