package main

import (
	"runtime/debug"
	"strings"
)

// version is stamped by the release build with the tag it is building, and is empty in every other build.
//
// Not a constant in the source. The tag lives in git, so writing it here as well would be a second place to
// update and a silent lie whenever only one of them moved. Stamping it from the workflow that already knows the
// tag keeps one source, and the workflow checks the binary reports what it was told.
var version string

// agoraVersion is what `agora --version` reports: the tag when there is one, and the commit it was built from
// otherwise, because "what exactly am I running" is the question a version answers.
func agoraVersion() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "devel"
	}
	// go install github.com/chancez/agora/cmd/agora@v1.2.3 records the tag here. A pseudo-version is not a tag:
	// it is generated for an untagged commit and carries the same hash reported below anyway.
	if v := info.Main.Version; isReleaseVersion(v) {
		return v
	}

	var revision, modified string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value
		}
	}
	if revision == "" {
		// No VCS stamps, from a module cache or -buildvcs=false. Whatever Main.Version says beats nothing,
		// including a pseudo-version, since that at least carries the commit.
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
		return "devel"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if modified == "true" {
		// Said out loud, because a binary built from a dirty tree is not the commit it names, and finding that
		// out from behaviour rather than from --version wastes an afternoon.
		return revision + "-dirty"
	}
	return revision
}

// isReleaseVersion is whether a module version is a tag somebody chose rather than one Go generated. A
// pseudo-version carries a timestamp and a hash, in the shape v0.0.0-20260824153000-abcdef123456.
func isReleaseVersion(v string) bool {
	return strings.HasPrefix(v, "v") && !strings.Contains(v, "-0.") && strings.Count(v, "-") < 2
}
