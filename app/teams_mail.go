package app

import "context"

// startTeamsMail starts the platform mail worker (verify / invite / reset letters from
// platform_email). Wave 1: a no-op; B3 fills it in.
func (a *App) startTeamsMail(ctx context.Context) (stop func(), err error) {
	return func() {}, nil
}
