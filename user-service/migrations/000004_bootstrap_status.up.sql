BEGIN;

  CREATE TABLE bootstrap_state (
      name text PRIMARY KEY,
      completed_at timestamptz
  );

  INSERT INTO bootstrap_state (name)
  VALUES ('bootstrap_is_completed');

  COMMIT;
