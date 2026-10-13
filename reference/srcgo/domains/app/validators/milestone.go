// Package validators holds the project's hand-written request checks —
// the `(w17.field).custom_validator` escape hatch.
//
// It is hand-written code in the project's OWN `srcgo/`, not in a
// generated bundle: the bundles under `w17/services/` are DO NOT EDIT
// and fully regenerated, so a rule that lives there would be erased by
// the next `w17ctl generate`. The generated storage bundle reaches this
// package by import — the annotation names it as
// `<go-import-path>:<FuncName>`, and codegen splices the call in
// verbatim ahead of every other rule on the field.
//
// The contract is deliberately narrow: `func(value <go-type>) error`,
// matching the field's Go type. Codegen does not reflect the function;
// it trusts the signature and a mismatch surfaces as a build error in
// the generated bundle. That keeps the codegen surface small at the
// cost of moving the error one step later, which is the right trade for
// an escape hatch.
package validators

import (
	"errors"
	"strings"
	"unicode"
)

// reservedMilestoneNames are the headings the milestone rollup report
// prints in its own right. A milestone carrying one of them makes the
// report ambiguous in a way no reader can resolve: the row and the
// summary line below it read identically.
//
// Stored NORMALISED (see normaliseMilestoneName) so the comparison is
// against the same shape on both sides.
var reservedMilestoneNames = map[string]bool{
	"total":      true,
	"subtotal":   true,
	"remaining":  true,
	"unassigned": true,
}

// RejectReservedMilestoneName refuses a milestone name that collides
// with a rollup heading once case and punctuation are ignored.
//
// This is the shape of rule the annotation surface cannot express, and
// the reason the escape hatch exists. `choices` matches an ALLOWED set
// exactly; this is a denied set. `pattern` matches the raw value; this
// compares a normalised one, so `Total`, `sub-total` and `TOTAL!` all
// have to fail while `Total Rewrite` — a different name that merely
// starts with the word — has to pass.
//
// An empty name is not this rule's business: `blank` already refuses
// it, and a validator that also refused it would report the wrong
// reason for a failure another rule owns.
func RejectReservedMilestoneName(value string) error {
	if value == "" {
		return nil
	}
	if reservedMilestoneNames[normaliseMilestoneName(value)] {
		return errors.New("milestone names cannot be one of the rollup report's own headings (total, subtotal, remaining, unassigned) — the report line and the milestone row would read identically")
	}
	return nil
}

// normaliseMilestoneName lower-cases and drops everything that is not a
// letter or a digit, so spacing and punctuation cannot be used to slip
// a reserved heading past the check.
func normaliseMilestoneName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
