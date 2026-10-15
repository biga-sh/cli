package version

import (
	"testing"

	"golang.org/x/mod/semver"
)

func TestVersionIsSemver(t *testing.T) {
	if v := String(); !semver.IsValid("v" + v) {
		t.Errorf("version %q is not a semantic version without a v prefix", v)
	}
}
