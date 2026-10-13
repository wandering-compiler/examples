// The auditsink plugin's Go module. It exists because `go_module` is
// mandatory (it is the canonical pb path) and codegen emits a `replace`
// at this directory — not because the plugin has authored handlers. It
// has none: auditsink is proto-only, a measuring instrument for the
// activation's connection-alias map.
module github.com/wandering-compiler/examples/reference/auditsink

go 1.26.1
