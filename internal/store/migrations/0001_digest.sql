CREATE TABLE unsubscribes (subject text PRIMARY KEY, at timestamptz NOT NULL DEFAULT now());

CREATE TABLE sends (
  subject text NOT NULL,
  week date NOT NULL,
  status text NOT NULL CHECK (status IN ('pending','sent','empty','failed','unsubscribed')),
  at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (subject, week)
);
