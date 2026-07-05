// Package reqctx carries authenticated identity through request contexts.
// It exists apart from server/handlers to avoid an import cycle.
package reqctx

import (
	"context"

	"github.com/google/uuid"

	"stor.app/api/internal/domain"
)

type ctxKey int

const (
	userIDKey ctxKey = iota
	membershipKey
)

// Membership is the caller's resolved household context, loaded fresh on every
// request (never trusted from the token) so removals apply immediately.
type Membership struct {
	HouseholdID       uuid.UUID
	UserID            uuid.UUID
	IsCreator         bool
	RequiresApprovals bool
	SplitMethod       domain.SplitMethod
	NeedsPercent      int32
	WantsPercent      int32
	SavingsPercent    int32
}

func WithUserID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

func UserID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userIDKey).(uuid.UUID)
	return id, ok
}

func WithMembership(ctx context.Context, m Membership) context.Context {
	return context.WithValue(ctx, membershipKey, m)
}

func MembershipFrom(ctx context.Context) (Membership, bool) {
	m, ok := ctx.Value(membershipKey).(Membership)
	return m, ok
}
