-- Each member's digest for a week as it was sent, for the web page. Deleted at expiry.
CREATE TABLE web_digests (
  subject text NOT NULL,
  week date NOT NULL,
  content jsonb NOT NULL,
  expires_at timestamptz NOT NULL,
  PRIMARY KEY (subject, week)
);
CREATE INDEX web_digests_expires_at ON web_digests (expires_at);
