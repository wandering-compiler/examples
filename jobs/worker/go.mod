module github.com/wandering-compiler/examples/jobs/worker

go 1.26.1

require (
	github.com/wandering-compiler/examples/jobs v0.0.0
	github.com/wandering-compiler/plugins/cluster v0.0.0
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.11
)

require (
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260803160001-6ac0973c030d // indirect
)

// The contract shared with the Jobs service, and the cluster plugin's worker
// library from the plugin tree vendored into this project.
replace (
	github.com/wandering-compiler/examples/jobs => ../srcgo
	github.com/wandering-compiler/plugins/cluster => ../proto/plugins/cluster/src
)
