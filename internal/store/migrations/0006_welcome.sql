-- The welcomes. Every row here but the starts and the unsubscribes is
-- deleted 21 days after it was written.

-- When welcomes first ran on each platform: nobody created before it is
-- ever welcomed.
CREATE TABLE welcome_starts (platform text PRIMARY KEY, at timestamptz NOT NULL);

-- Each newcomer welcomed: account is the Mbin user id or the MAS user id.
-- For Mbin, entry_id and url are the thread that named them, subscribed says
-- whether they were subscribed to the magazine, and day is the welcome email
-- that named them.
CREATE TABLE welcomed (
  platform text NOT NULL,
  account text NOT NULL,
  name text NOT NULL,
  mbin_id bigint,
  entry_id bigint,
  url text NOT NULL DEFAULT '',
  subscribed boolean NOT NULL DEFAULT false,
  day date,
  at timestamptz NOT NULL,
  PRIMARY KEY (platform, account)
);

-- Who got the welcome email on which day.
CREATE TABLE welcome_sends (
  subject text NOT NULL,
  day date NOT NULL,
  status text NOT NULL CHECK (status IN ('pending','sent','failed')),
  at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (subject, day)
);

CREATE TABLE welcome_unsubscribes (subject text PRIMARY KEY, at timestamptz NOT NULL DEFAULT now());

-- The last operator alert of each kind.
CREATE TABLE welcome_alerts (kind text PRIMARY KEY, day date NOT NULL, at timestamptz NOT NULL);
