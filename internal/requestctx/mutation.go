package requestctx

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

type mutationIDKey struct{}

func WithMutationID(ctx context.Context, mutationID uuid.UUID) context.Context {
	return context.WithValue(ctx, mutationIDKey{}, mutationID)
}

func WithMutationIDFromPayload(ctx context.Context, payload json.RawMessage) context.Context {
	var envelope struct {
		MutationID string `json:"client_mutation_id"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return ctx
	}
	mutationID, err := uuid.Parse(envelope.MutationID)
	if err != nil {
		return ctx
	}
	return WithMutationID(ctx, mutationID)
}

func MutationID(ctx context.Context) uuid.UUID {
	mutationID, _ := ctx.Value(mutationIDKey{}).(uuid.UUID)
	return mutationID
}
