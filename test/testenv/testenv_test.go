package testenv

import (
	"fmt"
	"strings"
	"testing"
)

// recorder stands in for *testing.T so that a skip or a failure can be observed instead of
// ending the test that runs Require.
type recorder struct {
	skipped string
	failed  string
}

func (r *recorder) Helper() {}

func (r *recorder) Skipf(format string, args ...any) { r.skipped = fmt.Sprintf(format, args...) }

func (r *recorder) Fatalf(format string, args ...any) { r.failed = fmt.Sprintf(format, args...) }

const variable = "TESTENV_TEST_DB_URI"

func TestRequire(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		ci       string
		wantSkip bool
		wantFail bool
	}{
		// A configured database is used whether or not the run is CI.
		{name: "set, developer run", value: "mongodb://db", ci: ""},
		{name: "set, CI run", value: "mongodb://db", ci: "true"},
		// A missing database skips on a developer's machine...
		{name: "missing, developer run", ci: "", wantSkip: true},
		{name: "missing, CI explicitly off", ci: "false", wantSkip: true},
		{name: "missing, CI=0", ci: "0", wantSkip: true},
		// ...and fails in CI, never skips.
		{name: "missing, GitHub Actions", ci: "true", wantFail: true},
		{name: "missing, CI=1", ci: "1", wantFail: true},
		{name: "missing, CI=TRUE", ci: "TRUE", wantFail: true},
		{name: "missing, CI unparsable", ci: "yes", wantFail: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(variable, tc.value)
			t.Setenv("CI", tc.ci)

			r := &recorder{}
			got := Require(r, variable, "this case needs a reachable MongoDB")

			if got != tc.value {
				t.Errorf("Require returned %q, want %q", got, tc.value)
			}
			if (r.skipped != "") != tc.wantSkip {
				t.Errorf("skipped = %q, want a skip: %v", r.skipped, tc.wantSkip)
			}
			if (r.failed != "") != tc.wantFail {
				t.Errorf("failed = %q, want a failure: %v", r.failed, tc.wantFail)
			}
		})
	}
}

// The CI failure must say which variable is missing and why the test needs it, since it is
// the only hint in a failed run.
func TestRequireFailureNamesTheVariableAndThePurpose(t *testing.T) {
	t.Setenv(variable, "")
	t.Setenv("CI", "true")

	r := &recorder{}
	Require(r, variable, "this case needs a reachable MongoDB")

	for _, want := range []string{variable, "this case needs a reachable MongoDB"} {
		if !strings.Contains(r.failed, want) {
			t.Errorf("failure message %q does not mention %q", r.failed, want)
		}
	}
}
