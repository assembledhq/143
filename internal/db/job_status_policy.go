package db

// The literal stays visible to the jobs-status inventory and PostgreSQL's
// partial-index planner. It is input ownership, never runnable/lease policy.
// Keep it coupled to JobStatus.IsNonterminalInputConsumer and the dedupe index.
const jobNonterminalInputConsumerStatusesSQL = `('pending', 'running', 'awaiting_start_control')`
