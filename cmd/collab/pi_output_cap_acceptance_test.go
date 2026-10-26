package main

import (
	"bytes"
	"os"
	"slices"
	"testing"
)

func TestPiOutputCapAcceptanceDefaultOverrideAndDisable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		want  string
	}{
		{"default", nil, "32768"},
		{"larger", []string{"--max-tokens", "65536"}, "65536"},
		{"minimum", []string{"--max-tokens=2048"}, "2048"},
		{"disabled", []string{"--max-tokens", "0"}, "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := parseStartForTest(t, append([]string{"pi"}, tc.flags...)...)
			plan := buildStartForTest(t, opts, defaultStartLayout())
			assertStartEqual(t, "request ceiling", piEnvValue(t, plan.Env, "PAIRTASK_PI_MAX_TOKENS"), tc.want)
			if slices.Contains(plan.Argv, "--max-tokens") {
				t.Fatal("pairtask flag leaked into native Pi arguments")
			}
			if plan.Config != nil || plan.ConfigPath != "" {
				t.Fatal("output cap must not create a models.json plan")
			}
		})
	}
}

func TestPiOutputCapAcceptanceRejectsInvalidAndOtherAgents(t *testing.T) {
	for _, value := range []string{"-1", "1", "2047", "2.5", "invalid", "32768oops", "9007199254740992", "9999999999999999999999999"} {
		if _, err := parseStart([]string{"pi", "--max-tokens", value}); err == nil {
			t.Errorf("accepted invalid cap %q", value)
		}
	}
	for _, agent := range []string{"codex", "claude"} {
		for _, value := range []string{"0", "32768"} {
			if _, err := parseStart([]string{agent, "--max-tokens", value}); err == nil {
				t.Errorf("%s accepted Pi-only flag %s", agent, value)
			}
		}
	}
}

func TestPiOutputCapAcceptanceExecutionReplacesInheritedCeiling(t *testing.T) {
	t.Setenv("PAIRTASK_PI_MAX_TOKENS", "900000")
	var out, stderr bytes.Buffer
	calls := 0
	rt := startRuntime{Stdout: &out, Stderr: &stderr, Layout: defaultStartLayout(), Exec: func(_ string, argv, env []string) error {
		calls++
		assertStartEqual(t, "one effective cap", piEnvValue(t, env, "PAIRTASK_PI_MAX_TOKENS"), "32768")
		assertStartEqual(t, "native arguments stay literal", argv[len(argv)-2:], []string{"--max-tokens", "99999"})
		return nil
	}}
	assertStartEqual(t, "launch: "+stderr.String(), runStart([]string{"pi", "--", "--max-tokens", "99999"}, rt), 0)
	assertStartEqual(t, "one launch", calls, 1)
	assertStartEqual(t, "parent environment untouched", os.Getenv("PAIRTASK_PI_MAX_TOKENS"), "900000")
}
