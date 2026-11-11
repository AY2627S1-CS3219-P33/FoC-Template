-- AI Assistance Disclosure:
-- Tool: ChatGPT (model: GPT-6 Luna), date: 2026-11-11
-- Scope: PostgreSQL schema migration and migration documentation for user-service.
-- Author review: I validated correctness, edited for style, and added boundary checks.

-- +goose Up

CREATE TABLE bootstrap_state (
      name text PRIMARY KEY,
      completed_at timestamptz
  );

  INSERT INTO bootstrap_state (name)
  VALUES ('bootstrap_is_completed');

-- +goose Down

DROP TABLE bootstrap_state;
