-- AI Assistance Disclosure:
-- Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
-- Scope: Implementation of SQL migration scripts for the team-designed schema.
-- Author review: Reviewed, validated, and tested by the authors.

-- +goose Up

-- Flags for couriers who complete errands unusually fast (ListCompletionCounts).
-- Partial, like errand_transitions_withdrawals_idx: it holds only the
-- completions that count (not admin_complete), so it stays small and the
-- query's identical WHERE lets Postgres use it.
CREATE INDEX errand_transitions_completions_idx
    ON errand_transitions (courier_id, at)
    WHERE event IN ('confirm', 'verification_lapsed');

-- +goose Down

DROP INDEX IF EXISTS errand_transitions_completions_idx;
