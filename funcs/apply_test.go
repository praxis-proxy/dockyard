package funcs

import "testing"

func TestKindMatcherExclude(t *testing.T) {
	// include=false keeps every kind except those listed.
	keep := kindMatcher([]string{"AITenant", "Secret"}, false)
	if keep("AITenant") || keep("Secret") {
		t.Error("excluded kinds should not be kept")
	}
	if !keep("Deployment") || !keep("Service") {
		t.Error("unlisted kinds should be kept under exclude")
	}
}

func TestKindMatcherInclude(t *testing.T) {
	// include=true keeps only the listed kinds.
	keep := kindMatcher([]string{"AITenant"}, true)
	if !keep("AITenant") {
		t.Error("listed kind should be kept under include")
	}
	if keep("Deployment") || keep("Secret") {
		t.Error("unlisted kinds should be dropped under include")
	}
}

func TestKindMatcherEmptySet(t *testing.T) {
	// Empty exclude set keeps everything; empty include set keeps nothing.
	if !kindMatcher(nil, false)("Anything") {
		t.Error("empty exclude set should keep all kinds")
	}
	if kindMatcher(nil, true)("Anything") {
		t.Error("empty include set should keep no kinds")
	}
}
