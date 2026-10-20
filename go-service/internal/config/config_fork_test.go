package config

import "testing"

func TestLauncherUpstreamVersionKeepsForkVersion(t *testing.T) {
	fork := Default().BuildVersion
	for _, tc := range []struct{ launcher, want string }{
		{"4.9.0", fork},
		{" 4.9.0 ", fork},
		{"4.9.1", "4.9.1"},
		{"4.8.1-test.9", "4.8.1-test.9"},
		{"4.9.0.7", "4.9.0.7"},
	} {
		if got := forkBuildVersion(tc.launcher, fork); got != tc.want {
			t.Errorf("forkBuildVersion(%q, %q) = %q, want %q", tc.launcher, fork, got, tc.want)
		}
	}
	t.Setenv("AC_BUILD_VERSION", "4.9.0")
	if got := Load().BuildVersion; got != fork {
		t.Errorf("Load with launcher version 4.9.0 = %q, want %q", got, fork)
	}
}
