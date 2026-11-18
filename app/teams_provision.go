package app

import "context"

// startProvisioner starts the team provisioner (spare databases, new teams). Wave 1: a no-op; B4
// fills it in.
func (a *App) startProvisioner(ctx context.Context) (stop func(), err error) {
	return func() {}, nil
}
