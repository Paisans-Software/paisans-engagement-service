-- Each member's Matrix setting, account and room; the digest bot's sends; and
-- the bot's own state.
CREATE TABLE matrix_members (
  subject text PRIMARY KEY,
  enabled boolean NOT NULL DEFAULT true,
  mas_user_id text,
  user_id text UNIQUE,
  room_id text UNIQUE,
  at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE dm_sends (
  subject text NOT NULL,
  week date NOT NULL,
  status text NOT NULL CHECK (status IN ('pending','sent','failed')),
  at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (subject, week)
);
CREATE TABLE bot_state (key text PRIMARY KEY, value text NOT NULL);
