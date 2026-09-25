BEGIN;

  CREATE TABLE bootstrap_state (
      name text PRIMARY KEY,
      completed_at timestamptz
  );

  INSERT INTO bootstrap_state (name)
  VALUES ('initial_super_admin');

  COMMIT;
