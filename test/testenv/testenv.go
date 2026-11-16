// Package testenv decides what a test does when an environment variable it depends on, such
// as the address of the test database, is not set.
package testenv

import (
	"os"
	"strconv"
)

// TB is the part of testing.TB that Require uses. *testing.T and *testing.B satisfy it.
type TB interface {
	Helper()
	Skipf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// Require returns the value of the environment variable name, which the calling test needs
// for purpose. When the variable is empty, a developer's run skips the test, while a CI run
// fails it: there a skip would turn a missing database service or a misspelt variable into a
// green run that tested nothing.
//
// The run counts as CI when CI is set to anything but a false value; GitHub Actions sets
// CI=true. An unparsable value counts as CI, so that a typo cannot reopen the skip.
func Require(t TB, name, purpose string) string {
	t.Helper()
	if value := os.Getenv(name); value != "" {
		return value
	}
	if inCI() {
		t.Fatalf("%s is not set: %s; CI=%q, so the test fails instead of skipping", name, purpose, os.Getenv("CI"))
		return ""
	}
	t.Skipf("%s is not set: %s", name, purpose)
	return ""
}

func inCI() bool {
	value := os.Getenv("CI")
	if value == "" {
		return false
	}
	on, err := strconv.ParseBool(value)
	return err != nil || on
}
