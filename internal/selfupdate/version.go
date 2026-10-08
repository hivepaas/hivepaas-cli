package selfupdate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// versionPattern is a release's version as its tag says it: v0.2.0, or
// v0.3.0-beta1 for a pre-release - the server's form, without a dot before the
// number. The v may be left out: the CLI's own version, from goreleaser, has none.
var versionPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([a-z]+)(\d+))?$`)

// Version is a release's version.
type Version struct {
	Major, Minor, Patch int
	// Pre and PreNum are a pre-release's name and number: beta, 1. Empty for a
	// release.
	Pre    string
	PreNum int
}

// ParseVersion reads a version such as v0.3.0-beta1.
func ParseVersion(s string) (Version, error) {
	m := versionPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Version{}, fmt.Errorf("%q is not a version such as v1.2.3 or v1.2.3-beta1", s) //nolint:err113
	}
	var v Version
	v.Major, _ = strconv.Atoi(m[1])
	v.Minor, _ = strconv.Atoi(m[2])
	v.Patch, _ = strconv.Atoi(m[3])
	v.Pre = m[4]
	if m[5] != "" {
		v.PreNum, _ = strconv.Atoi(m[5])
	}
	return v, nil
}

// IsPrerelease says v is a beta, not a release.
func (v Version) IsPrerelease() bool { return v.Pre != "" }

// String is v as a tag: v0.3.0-beta1.
func (v Version) String() string {
	s := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += fmt.Sprintf("-%s%d", v.Pre, v.PreNum)
	}
	return s
}

// Compare is -1, 0 or 1 as v is older than, the same as, or newer than o. A
// pre-release comes before its release: v1.0.0-beta2 < v1.0.0.
func (v Version) Compare(o Version) int {
	for _, d := range []int{v.Major - o.Major, v.Minor - o.Minor, v.Patch - o.Patch} {
		if d != 0 {
			return sign(d)
		}
	}
	switch {
	case v.Pre == o.Pre:
		return sign(v.PreNum - o.PreNum)
	case v.Pre == "":
		return 1
	case o.Pre == "":
		return -1
	case v.Pre < o.Pre:
		return -1
	default:
		return 1
	}
}

func sign(d int) int {
	switch {
	case d < 0:
		return -1
	case d > 0:
		return 1
	}
	return 0
}
