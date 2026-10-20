package canonpack

import (
	"regexp"
	"strings"
)

// forkVersionRE is a fork build: the upstream release it is based on, then
// the fork revision (4.9.0.1).
var forkVersionRE = regexp.MustCompile(`^((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))\.(?:0|[1-9][0-9]*)$`)

// upstreamVersion is the release a running version is compatible as: a fork
// build counts as the upstream release it is based on.
func upstreamVersion(current string) string {
	if m := forkVersionRE.FindStringSubmatch(strings.TrimSpace(current)); m != nil {
		return m[1]
	}
	return current
}
