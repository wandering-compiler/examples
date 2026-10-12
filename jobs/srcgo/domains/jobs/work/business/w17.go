// Package business is the jobs domain's hand-written tier: the Jobs service.
// The generated jobs-business bundle builds the clients and the registry and
// hands them to RegisterBusiness.
package business

import (
	jobsbiz "github.com/wandering-compiler/examples/jobs/w17/stubs/business/jobs"
)

// RegisterBusiness registers the Jobs service on the bundle.
func RegisterBusiness(_ *jobsbiz.EnvConfig, registry jobsbiz.HandlerRegistry, clients jobsbiz.ClientSet) error {
	registry.RegisterJobsServer(&jobs{cluster: clients.ClusterService()})
	return nil
}
