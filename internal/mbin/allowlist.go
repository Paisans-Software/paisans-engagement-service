package mbin

// Allowlist is every table and column the digest reads from Mbin, and the
// only ones its database role is granted SELECT on. Mbin keeps credentials
// in its database (sessions, remember me and OAuth tokens, and the password
// hash and TOTP secrets on "user"), so the grant names columns one by one
// and never a whole table that holds a secret.
//
// The toolkit grants the role from its own copy of this list, which must
// match it exactly: a query here that reads a column missing from that copy
// fails at runtime rather than widening the grant.
var Allowlist = map[string][]string{
	"entry":                 {"id", "user_id", "magazine_id", "slug", "title", "comment_count", "score", "is_adult", "visibility", "ap_id", "created_at", "last_active"},
	"entry_comment":         {"id", "user_id", "entry_id", "magazine_id", "body", "up_votes", "favourite_count", "visibility", "is_adult", "created_at"},
	"post":                  {"id", "user_id", "magazine_id", "slug", "body", "comment_count", "score", "is_adult", "visibility", "ap_id", "created_at", "last_active"},
	"post_comment":          {"id", "user_id", "post_id", "magazine_id", "body", "up_votes", "favourite_count", "visibility", "is_adult", "created_at"},
	"entry_vote":            {"user_id", "entry_id", "choice"},
	"post_vote":             {"user_id", "post_id", "choice"},
	"favourite":             {"user_id", "entry_id", "post_id"},
	"bookmark":              {"user_id", "entry_id", "post_id"},
	"notification":          {"user_id", "status", "entry_id", "entry_comment_id", "post_id", "post_comment_id"},
	"magazine":              {"id", "name", "title", "ap_id", "is_adult", "visibility", "marked_for_deletion_at", "ap_deleted_at"},
	"magazine_subscription": {"user_id", "magazine_id"},
	"magazine_block":        {"user_id", "magazine_id"},
	"magazine_ban":          {"user_id", "magazine_id", "expired_at"},
	"user_block":            {"blocker_id", "blocked_id"},
	"user":                  {"id", "username", "email", "is_verified", "is_banned", "is_deleted", "marked_for_deletion_at", "visibility", "type", "ap_id", "oauth_oidc_id", "last_active", "created_at"},
}
