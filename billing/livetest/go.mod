module github.com/wandering-compiler/examples/billing/livetest

go 1.26.1

require (
	github.com/lib/pq v1.12.3
	github.com/wandering-compiler/examples/billing/w17/stubs v0.0.0
	github.com/wandering-compiler/sdk/go v0.1.0-rc.52
	google.golang.org/grpc v1.83.2
)

require (
	github.com/go-jose/go-jose/v4 v4.1.5 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260803160001-6ac0973c030d // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

// The generated pb stubs: `w17ctl codegen` writes them, they are not committed.
replace github.com/wandering-compiler/examples/billing/w17/stubs => ../w17/stubs
