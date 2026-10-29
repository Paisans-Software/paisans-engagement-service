package mbin

// Every statement the service runs against Mbin. Table names are never schema
// qualified, so the connection's search_path picks the schema. Every column
// named here must be in Allowlist.

// memberRule is who counts as a member: a local, live person with a Pocket ID
// subject to name them by. An address is mailed only when verified, which
// membersSQL decides. The alias u must be "user".
const memberRule = `u.ap_id IS NULL AND NOT u.is_banned AND NOT u.is_deleted
	AND u.marked_for_deletion_at IS NULL AND u.visibility = 'visible' AND u.type = 'Person'
	AND u.oauth_oidc_id IS NOT NULL`

const membersSQL = `
SELECT u.id, u.oauth_oidc_id, CASE WHEN u.is_verified THEN u.email END, u.username
FROM "user" u
WHERE ` + memberRule + `
ORDER BY u.id`

// openMagazine is a magazine m whose threads the digest may show and name:
// visible, not deleted here or on its home server, not marked for deletion
// and not adult.
const openMagazine = `m.visibility = 'visible' AND m.ap_deleted_at IS NULL
	AND m.marked_for_deletion_at IS NULL AND NOT m.is_adult`

// activeBan is a magazine_ban row still in force: a ban with an expiry in the
// past no longer applies.
const activeBan = `(expired_at IS NULL OR expired_at > now())`

// isMemberSQL is whether a Pocket ID subject belongs to a member. $1 is the subject.
const isMemberSQL = `
SELECT EXISTS (SELECT 1 FROM "user" u WHERE u.oauth_oidc_id = $1 AND ` + memberRule + `)`

// hotSQL is the week's candidate threads, entries and posts alike, with their
// figures and, for local items, their top three comments. $1 and $2 are the
// window's start and end. Columns: kind, id, author, slug, title or body,
// score, comment_count, federated, magazine id, name, title, participants,
// the week's comment count, top comments and the week's top comments as JSON.
const hotSQL = `
WITH
-- Magazines a member follows: the only way a federated item gets in.
followed AS (
	SELECT DISTINCT s.magazine_id
	FROM magazine_subscription s
	JOIN "user" u ON u.id = s.user_id
	WHERE ` + memberRule + `
),
-- Every visible, non adult item that was active this week and existed
-- before it ended.
items AS (
	SELECT 'entry' AS kind, e.id, e.user_id, e.magazine_id, e.slug, e.title AS text,
		e.score, e.comment_count, e.ap_id IS NOT NULL AS federated
	FROM entry e
	WHERE e.last_active >= $1 AND e.created_at < $2 AND e.visibility = 'visible' AND NOT e.is_adult
	UNION ALL
	SELECT 'post', p.id, p.user_id, p.magazine_id, p.slug, p.body,
		p.score, p.comment_count, p.ap_id IS NOT NULL
	FROM post p
	WHERE p.last_active >= $1 AND p.created_at < $2 AND p.visibility = 'visible' AND NOT p.is_adult
),
-- The items that pass the magazine, author and federation rules, cut to
-- the 200 best before the per item subqueries below run.
candidates AS (
	SELECT i.*, m.name AS magazine_name, m.title AS magazine_title
	FROM items i
	JOIN magazine m ON m.id = i.magazine_id
		AND ` + openMagazine + `
	JOIN "user" a ON a.id = i.user_id AND NOT a.is_banned AND NOT a.is_deleted
	WHERE NOT i.federated OR i.magazine_id IN (SELECT magazine_id FROM followed)
	ORDER BY i.score + i.comment_count DESC, i.id, i.kind
	LIMIT 200
)
SELECT c.kind, c.id, c.user_id, c.slug, c.text, c.score, c.comment_count, c.federated,
	c.magazine_id, c.magazine_name, c.magazine_title,
	-- The author plus everyone who left a visible comment this week.
	(SELECT count(DISTINCT who) FROM (
		SELECT c.user_id AS who
		UNION ALL
		SELECT ec.user_id FROM entry_comment ec
		WHERE c.kind = 'entry' AND ec.entry_id = c.id AND ec.visibility = 'visible'
			AND ec.created_at >= $1 AND ec.created_at < $2
		UNION ALL
		SELECT pc.user_id FROM post_comment pc
		WHERE c.kind = 'post' AND pc.post_id = c.id AND pc.visibility = 'visible'
			AND pc.created_at >= $1 AND pc.created_at < $2
	) people) AS participants,
	-- Every visible comment posted this week, whoever wrote it.
	(SELECT count(*) FROM entry_comment ec
		WHERE c.kind = 'entry' AND ec.entry_id = c.id AND ec.visibility = 'visible'
			AND ec.created_at >= $1 AND ec.created_at < $2)
	+ (SELECT count(*) FROM post_comment pc
		WHERE c.kind = 'post' AND pc.post_id = c.id AND pc.visibility = 'visible'
			AND pc.created_at >= $1 AND pc.created_at < $2) AS new_comments,
	top.comments, top.new_comments
FROM candidates c
-- The three best liked visible, non adult comments by live local accounts,
-- of all time and of this week, for local items only: a federated item is
-- never quoted.
LEFT JOIN LATERAL (
	WITH cm AS (
		SELECT cm.id, cm.user_id, cm.body, cm.votes, cm.created_at FROM (
			SELECT ec.id, ec.user_id, ec.body, ec.up_votes + ec.favourite_count AS votes,
				ec.visibility, ec.is_adult, ec.created_at
			FROM entry_comment ec WHERE c.kind = 'entry' AND ec.entry_id = c.id
			UNION ALL
			SELECT pc.id, pc.user_id, pc.body, pc.up_votes + pc.favourite_count,
				pc.visibility, pc.is_adult, pc.created_at
			FROM post_comment pc WHERE c.kind = 'post' AND pc.post_id = c.id
		) cm
		JOIN "user" u ON u.id = cm.user_id AND u.ap_id IS NULL AND NOT u.is_banned AND NOT u.is_deleted
		WHERE cm.visibility = 'visible' AND NOT cm.is_adult
	)
	SELECT
		(SELECT json_agg(json_build_object('author', t.user_id, 'body', t.body, 'votes', t.votes)
			ORDER BY t.votes DESC, t.id)
		FROM (SELECT * FROM cm ORDER BY votes DESC, id LIMIT 3) t) AS comments,
		(SELECT json_agg(json_build_object('author', t.user_id, 'body', t.body, 'votes', t.votes)
			ORDER BY t.votes DESC, t.id)
		FROM (SELECT * FROM cm WHERE created_at >= $1 AND created_at < $2
			ORDER BY votes DESC, id LIMIT 3) t) AS new_comments
) top ON NOT c.federated
ORDER BY c.score + c.comment_count DESC, c.id, c.kind`

// unansweredSQL is the week's new local threads, entries and posts alike,
// that nobody but their author has commented on, voted on, boosted or
// favourited, the newest 200. $1 and $2 are the window's start and end.
// Columns: kind, id, author, slug, title or body, magazine id, name, title.
const unansweredSQL = `
WITH items AS (
	SELECT 'entry' AS kind, e.id, e.user_id, e.magazine_id, e.slug, e.title AS text, e.created_at
	FROM entry e
	WHERE e.created_at >= $1 AND e.created_at < $2 AND e.ap_id IS NULL
		AND e.visibility = 'visible' AND NOT e.is_adult
		AND NOT EXISTS (SELECT 1 FROM entry_comment ec WHERE ec.entry_id = e.id
			AND ec.user_id <> e.user_id AND ec.visibility = 'visible')
		-- Choice 0 is a vote taken back.
		AND NOT EXISTS (SELECT 1 FROM entry_vote v WHERE v.entry_id = e.id
			AND v.user_id <> e.user_id AND v.choice <> 0)
		AND NOT EXISTS (SELECT 1 FROM favourite f WHERE f.entry_id = e.id AND f.user_id <> e.user_id)
	UNION ALL
	SELECT 'post', p.id, p.user_id, p.magazine_id, p.slug, p.body, p.created_at
	FROM post p
	WHERE p.created_at >= $1 AND p.created_at < $2 AND p.ap_id IS NULL
		AND p.visibility = 'visible' AND NOT p.is_adult
		AND NOT EXISTS (SELECT 1 FROM post_comment pc WHERE pc.post_id = p.id
			AND pc.user_id <> p.user_id AND pc.visibility = 'visible')
		AND NOT EXISTS (SELECT 1 FROM post_vote v WHERE v.post_id = p.id
			AND v.user_id <> p.user_id AND v.choice <> 0)
		AND NOT EXISTS (SELECT 1 FROM favourite f WHERE f.post_id = p.id AND f.user_id <> p.user_id)
)
SELECT i.kind, i.id, i.user_id, i.slug, i.text, m.id, m.name, m.title
FROM items i
JOIN magazine m ON m.id = i.magazine_id
	AND ` + openMagazine + `
JOIN "user" a ON a.id = i.user_id AND NOT a.is_banned AND NOT a.is_deleted
ORDER BY i.created_at DESC, i.id, i.kind
LIMIT 200`

// choicesSQL is what the member has chosen about magazines and people: each
// row is a set name, an id and, for the magazines they follow and can open,
// the magazine's name and title, which are empty on every other row. $1 is the member's id, $2 the window's start.
const choicesSQL = `
SELECT 'subscribed', magazine_id, '', '' FROM magazine_subscription WHERE user_id = $1
UNION ALL
-- The followed magazines the digest may name: those hotSQL would take from
-- and the member is not banned from.
SELECT 'following', m.id, COALESCE(m.name, ''), COALESCE(m.title, '')
FROM magazine_subscription s
JOIN magazine m ON m.id = s.magazine_id
	AND ` + openMagazine + `
WHERE s.user_id = $1 AND NOT EXISTS (
	SELECT 1 FROM magazine_ban b WHERE b.user_id = $1 AND b.magazine_id = m.id
		AND ` + activeBan + `)
UNION ALL
SELECT 'blocked_magazine', magazine_id, '', '' FROM magazine_block WHERE user_id = $1
UNION ALL
SELECT 'banned', magazine_id, '', '' FROM magazine_ban
WHERE user_id = $1 AND ` + activeBan + `
UNION ALL
SELECT 'blocked_user', blocked_id, '', '' FROM user_block WHERE blocker_id = $1
UNION ALL
-- Mbin's only visit signal, updated at most daily.
SELECT 'visited', id, '', '' FROM "user" WHERE id = $1 AND last_active >= $2`

// joinedSQL is which of the given items the member has already taken part
// in. $1 is the member's id, $2 the entry ids and $3 the post ids asked about.
// Each branch is one signal; UNION removes the repeats.
const joinedSQL = `
SELECT 'entry', id FROM entry WHERE user_id = $1 AND id = ANY($2)
UNION
SELECT 'entry', entry_id FROM entry_comment WHERE user_id = $1 AND entry_id = ANY($2)
UNION
-- Choice 0 is a vote taken back.
SELECT 'entry', entry_id FROM entry_vote WHERE user_id = $1 AND entry_id = ANY($2) AND choice <> 0
UNION
SELECT 'entry', entry_id FROM favourite WHERE user_id = $1 AND entry_id = ANY($2)
UNION
SELECT 'entry', entry_id FROM bookmark WHERE user_id = $1 AND entry_id = ANY($2)
UNION
-- A notification the member opened, about the entry or one of its comments.
SELECT 'entry', coalesce(n.entry_id, ec.entry_id)
FROM notification n LEFT JOIN entry_comment ec ON ec.id = n.entry_comment_id
WHERE n.user_id = $1 AND n.status = 'read' AND coalesce(n.entry_id, ec.entry_id) = ANY($2)
UNION
SELECT 'post', id FROM post WHERE user_id = $1 AND id = ANY($3)
UNION
SELECT 'post', post_id FROM post_comment WHERE user_id = $1 AND post_id = ANY($3)
UNION
SELECT 'post', post_id FROM post_vote WHERE user_id = $1 AND post_id = ANY($3) AND choice <> 0
UNION
SELECT 'post', post_id FROM favourite WHERE user_id = $1 AND post_id = ANY($3)
UNION
SELECT 'post', post_id FROM bookmark WHERE user_id = $1 AND post_id = ANY($3)
UNION
SELECT 'post', coalesce(n.post_id, pc.post_id)
FROM notification n LEFT JOIN post_comment pc ON pc.id = n.post_comment_id
WHERE n.user_id = $1 AND n.status = 'read' AND coalesce(n.post_id, pc.post_id) = ANY($3)`

// statsSQL is the week's local figures; $1 and $2 are the window. Comments
// carry no ap_id of their own, so a local comment is one by a local account.
// Removed content is not counted.
const statsSQL = `
SELECT
	(SELECT count(*) FROM entry WHERE ap_id IS NULL AND visibility = 'visible'
		AND created_at >= $1 AND created_at < $2)
	+ (SELECT count(*) FROM post WHERE ap_id IS NULL AND visibility = 'visible'
		AND created_at >= $1 AND created_at < $2),
	(SELECT count(*) FROM entry_comment ec JOIN "user" u ON u.id = ec.user_id
		WHERE u.ap_id IS NULL AND ec.visibility = 'visible' AND ec.created_at >= $1 AND ec.created_at < $2)
	+ (SELECT count(*) FROM post_comment pc JOIN "user" u ON u.id = pc.user_id
		WHERE u.ap_id IS NULL AND pc.visibility = 'visible' AND pc.created_at >= $1 AND pc.created_at < $2),
	(SELECT count(*) FROM "user" u WHERE ` + memberRule + ` AND u.created_at >= $1 AND u.created_at < $2)`
