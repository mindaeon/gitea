// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package user

import (
	"context"
	"fmt"

	audit_model "gitea.dev/models/audit"
	auth_model "gitea.dev/models/auth"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/session"
	"gitea.dev/services/audit"
)

// SignOutEverywhere ends every web session of the user and revokes their remembered
// sign-ins ("remember this device"), so that the user has to sign in again everywhere.
// Access tokens, OAuth2 grants and keys are left as they are.
// It returns session.ErrUserSessionsUnsupported, having changed nothing, when the
// configured session provider can't find the sessions of one user.
func SignOutEverywhere(ctx context.Context, u *user_model.User) (int, error) {
	if !session.CanDestroyUserSessions() {
		return 0, session.ErrUserSessionsUnsupported
	}
	// the remembered sign-ins first: one used meanwhile would start a new session
	if err := auth_model.DeleteAuthTokensByUserID(ctx, u.ID); err != nil {
		return 0, fmt.Errorf("DeleteAuthTokensByUserID: %w", err)
	}
	destroyed, err := session.DestroyUserSessions(ctx, u.ID)
	if err != nil {
		// the remembered sign-ins are gone and some sessions may be: that is recorded too
		audit.Record(ctx, audit_model.UserSessionsDestroy, u, "sessions", destroyed, "error", err.Error())
		return destroyed, err
	}
	audit.Record(ctx, audit_model.UserSessionsDestroy, u, "sessions", destroyed)
	return destroyed, nil
}
