package main

import (
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/services/feedback"
	"github.com/rs/zerolog"
)

// newWorkerFeedbackService wires the already-produced review feedback jobs.
func newWorkerFeedbackService(pool db.TxStarter, jobs feedback.JobStore, llm feedback.LLMClient, logger zerolog.Logger) *feedback.Service {
	memories := db.NewMemoryStore(pool)
	return feedback.NewService(db.NewReviewCommentStore(pool), memories, jobs, llm, logger)
}
