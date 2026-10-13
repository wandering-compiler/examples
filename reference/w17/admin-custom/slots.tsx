// Project-owned admin SPA slots — the escape hatch behind
// `(w17.admin_api).custom_js` (declared in
// proto/domains/app/admin.proto).
//
// This directory is NOT generated and NOT rewritten by a regen. That is
// the whole point of it: the generated `spa/src/main.tsx` carries a
// `DO NOT EDIT` header and is replaced wholesale on every codegen, so a
// slot map edited into it is lost the next time anyone runs one. The
// generated entry imports this module and spreads it over its own
// (empty) registry, so anything declared here wins.
//
// The contract is the module name and the export name: `<custom_js>/slots`
// exporting `slots`. Nothing else about this file is prescribed.
import { listColumnSlotKey } from "@w17/admin-runtime";
import type { SlotRegistry } from "@w17/admin-runtime";

// One slot, chosen because its effect is VISIBLE IN THE DOM and not
// reproducible by any declaration: TaskAdmin's `priority` column is an
// int32, and the runtime's default renderer prints the number. This
// renders a word instead, which no `(w17.field).format` or
// `AdminColumn.format` can produce — those localise a value, they do not
// replace it with a different vocabulary.
//
// It is also the column carrying `label: "Urgency"`, so the two
// mechanisms sit on the same cell and stay distinguishable: the label is
// the HEADER (declared, in admin.json), this is the CELL (project code,
// in the bundle).
const PriorityWord = ({ value }: { value: unknown }) => {
  const n = typeof value === "number" ? value : Number(value ?? 0);
  if (n >= 8) return <span data-testid="priority-word">critical</span>;
  if (n >= 4) return <span data-testid="priority-word">normal</span>;
  return <span data-testid="priority-word">low</span>;
};

export const slots: SlotRegistry = {
  [listColumnSlotKey("TaskAdmin", "priority")]: PriorityWord,
};
