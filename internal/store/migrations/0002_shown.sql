-- Which threads each member's digest showed, so a thread that comes back is
-- shown by its new activity. kind is the source's own name for the item's
-- table.
CREATE TABLE shown (
  subject text NOT NULL,
  week date NOT NULL,
  kind text NOT NULL,
  item_id bigint NOT NULL,
  PRIMARY KEY (subject, kind, item_id, week)
);
