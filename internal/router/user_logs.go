package router

import (
	"context"
	"errors"

	"replica/internal/service"

	"github.com/danielgtaylor/huma/v2"
)

// userLogAudit builds the user log context for an authenticated admin API caller.
func userLogAudit(ctx context.Context, caller *service.AuthenticatedUser) service.AuditContext {
	audit := service.AuditContext{SourceAddress: service.SourceAddress(ctx)}
	if caller != nil {
		id := caller.ID
		audit.ActorUserID = &id
	}
	return audit
}

// mapLoggedPermissionError maps an Authorize error and records permission denials of
// authenticated callers with logDenied.
func mapLoggedPermissionError(ctx context.Context, err error, caller *service.AuthenticatedUser, logDenied func(service.AuditContext) error) error {
	if errors.Is(err, service.ErrForbidden) && caller != nil {
		if logErr := logDenied(userLogAudit(ctx, caller)); logErr != nil {
			return huma.Error500InternalServerError("permission check failed", logErr)
		}
	}
	return mapPermissionError(err)
}
