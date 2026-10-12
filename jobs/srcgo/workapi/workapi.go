// Package workapi is the contract between the Jobs service and the workers:
// one gRPC method, jobs.Worker/Shout, with a string in and a string out.
//
// The relay forwards a call without reading it, so the contract is the two
// ends' business alone. It is written as a ServiceDesc over well-known types
// rather than generated from a .proto, because the project's protos are what
// w17 serves, and this service is served by the workers, not by the project.
package workapi

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// ShoutMethod is the full method name a caller invokes.
const ShoutMethod = "/jobs.Worker/Shout"

// WorkerHeader is the response header naming the worker that ran the job.
const WorkerHeader = "x-jobs-worker"

// Worker is what a worker implements.
type Worker interface {
	Shout(ctx context.Context, text *wrapperspb.StringValue) (*wrapperspb.StringValue, error)
}

// Register serves w on s.
func Register(s grpc.ServiceRegistrar, w Worker) {
	s.RegisterService(&grpc.ServiceDesc{
		ServiceName: "jobs.Worker",
		HandlerType: (*Worker)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Shout",
			Handler: func(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
				in := &wrapperspb.StringValue{}
				if err := dec(in); err != nil {
					return nil, err
				}
				return srv.(Worker).Shout(ctx, in)
			},
		}},
	}, w)
}

// Shout calls a worker over conn and returns its answer and its name.
func Shout(ctx context.Context, conn grpc.ClientConnInterface, text string, opts ...grpc.CallOption) (answer, worker string, err error) {
	var header metadata.MD
	out := &wrapperspb.StringValue{}
	opts = append(opts, grpc.Header(&header))
	if err := conn.Invoke(ctx, ShoutMethod, wrapperspb.String(text), out, opts...); err != nil {
		return "", "", err
	}
	if v := header.Get(WorkerHeader); len(v) > 0 {
		worker = v[0]
	}
	return out.GetValue(), worker, nil
}
