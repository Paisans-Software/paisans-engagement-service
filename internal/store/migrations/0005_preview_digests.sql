-- Each member's latest preview digest for a week, for the preview link in a
-- preview notice. Apart from web_digests, so a preview is never the member's
-- digest for the week. Deleted at expiry.
CREATE TABLE preview_digests (
  subject text NOT NULL,
  week date NOT NULL,
  content jsonb NOT NULL,
  expires_at timestamptz NOT NULL,
  PRIMARY KEY (subject, week)
);
CREATE INDEX preview_digests_expires_at ON preview_digests (expires_at);
