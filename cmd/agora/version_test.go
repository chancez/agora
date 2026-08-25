package main

import (
	"strings"
	"testing"
)

// TestVersionSaysWhatItIs is the question a version answers: which build is this. A tag when the release stamped
// one, and the commit otherwise, so two builds of the same day can be told apart.
func TestVersionSaysWhatItIs(t *testing.T) {
	t.Run("stamped by a release", func(t *testing.T) {
		version = "v1.2.3"
		t.Cleanup(func() { version = "" })
		if got := agoraVersion(); got != "v1.2.3" {
			t.Errorf("agoraVersion() = %q, want the stamped tag", got)
		}
	})

	t.Run("from the build itself", func(t *testing.T) {
		// Whatever the toolchain recorded, but never empty: a blank --version is indistinguishable from a
		// command that does not have one.
		if got := agoraVersion(); got == "" {
			t.Error("agoraVersion() is empty")
		}
	})
}

func TestVersionFlagPrintsIt(t *testing.T) {
	c := newCLI(t)
	got := c.mustRun("--version")
	if !strings.Contains(got.stdout, agoraVersion()) {
		t.Errorf("agora --version printed %q, want it to contain %q", got.stdout, agoraVersion())
	}
}

func TestIsReleaseVersion(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{version: "v1.2.3", want: true},
		{version: "v0.1.0", want: true},
		// A pre-release tag is still a tag somebody chose.
		{version: "v1.0.0-rc1", want: true},
		// What Go generates for an untagged commit, which carries the same hash reported anyway.
		{version: "v0.0.0-20260824153000-abcdef123456"},
		{version: "(devel)"},
		{version: ""},
	} {
		t.Run(tc.version, func(t *testing.T) {
			if got := isReleaseVersion(tc.version); got != tc.want {
				t.Errorf("isReleaseVersion(%q) = %v, want %v", tc.version, got, tc.want)
			}
		})
	}
}
