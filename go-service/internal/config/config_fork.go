package config

import "strings"

// forkBuildVersion keeps the fork's own version when the launcher passes the
// upstream release it builds on: the installed package still names 4.9.0 while
// this binary is 4.9.0.N. Any other version the launcher passes wins.
func forkBuildVersion(launcher, fork string) string {
	launcher = strings.TrimSpace(launcher)
	if strings.HasPrefix(fork, launcher+".") {
		return fork
	}
	return launcher
}
