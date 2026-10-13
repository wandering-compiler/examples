package validators

import "testing"

// The escape hatch's own unit test. The generated bundle proves the
// call is WIRED (the e2e case refuses "Sub-Total" over REST); this
// proves the rule itself, including the two edges an author is most
// likely to get wrong: a name that merely CONTAINS a reserved word must
// pass, and normalisation must not be defeatable by punctuation.
func TestRejectReservedMilestoneName(t *testing.T) {
	refuse := []string{
		"total",
		"Total",
		"TOTAL!",
		"sub-total",
		"Sub Total",
		"  remaining  ",
		"un.assigned",
	}
	for _, name := range refuse {
		if err := RejectReservedMilestoneName(name); err == nil {
			t.Errorf("RejectReservedMilestoneName(%q) = nil, want a refusal — punctuation and case must not get a rollup heading past the check", name)
		}
	}

	accept := []string{
		"",               // `blank` owns the empty case, not this rule
		"Total Rewrite",  // contains a heading, is not one
		"alpha release",  // the ordinary case
		"remaining work", // ditto
		"subtotals",      // a different word
	}
	for _, name := range accept {
		if err := RejectReservedMilestoneName(name); err != nil {
			t.Errorf("RejectReservedMilestoneName(%q) = %v, want nil — the rule refuses names that ARE a heading, not names that mention one", name, err)
		}
	}
}
