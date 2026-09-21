package worker

import (
	"reflect"
	"testing"
)

func TestCommandShellUnix(t *testing.T) {
	gotExe, gotArgs := commandShell("linux", "printf ok")
	if gotExe != "sh" {
		t.Fatalf("exe = %q, want sh", gotExe)
	}
	want := []string{"-c", "printf ok"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("args = %#v, want %#v", gotArgs, want)
	}
}

func TestCommandShellWindows(t *testing.T) {
	gotExe, gotArgs := commandShell("windows", "Write-Output ok")
	if gotExe != "powershell.exe" {
		t.Fatalf("exe = %q, want powershell.exe", gotExe)
	}
	want := []string{
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-Command",
		"Write-Output ok; $ag_ok=$?; $ag_ec=$LASTEXITCODE; if (-not $ag_ok) { if ($null -ne $ag_ec -and $ag_ec -ne 0) { exit $ag_ec }; exit 1 }; if ($null -ne $ag_ec) { exit $ag_ec }; exit 0",
	}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("args = %#v, want %#v", gotArgs, want)
	}
}

func TestCommandShellUnknownFallsBackToPOSIX(t *testing.T) {
	gotExe, gotArgs := commandShell("plan9", "echo safe")
	if gotExe != "sh" || len(gotArgs) != 2 || gotArgs[0] != "-c" || gotArgs[1] != "echo safe" {
		t.Fatalf("unexpected fallback: exe=%q args=%#v", gotExe, gotArgs)
	}
}
