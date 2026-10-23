module github.com/wandering-compiler/examples/jobs/register

go 1.26.1

require (
	github.com/wandering-compiler/examples/jobs/w17/stubs v0.0.0
	github.com/wandering-compiler/sdk/go v0.1.0-rc.36
	google.golang.org/grpc v1.83.2
)

require (
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260803160001-6ac0973c030d // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

// The generated pb stubs: `w17ctl codegen` writes them, they are not committed.
replace github.com/wandering-compiler/examples/jobs/w17/stubs => ../w17/stubs
