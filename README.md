# paisans-engagement-service

paisans-engagement-service sends the community's weekly digest email, a small Go service run by the `engagement` blueprint of [Paisans](https://github.com/Paisans-Software/paisans-cli), which renders the environment below. Every Monday at 08:00 in the container's local time, it emails each member the threads and microblog posts from the past seven days that they have not joined yet: a heading with the week's dates, three figures for the whole community (new threads, comments and new members), one featured thread, a short list of more, and a button to the Mbin front page. There is no sign up and no setting: the schedule is fixed, and a member who wants no more mail follows the link at the bottom of the email.

* **Who gets it.** Local Mbin accounts with a verified email address and an OIDC subject. Banned, deleted, unverified, bot and remote accounts get nothing, and neither does an address that unsubscribed. A member is identified everywhere in the service's own database and in its tokens by Mbin's `"user".oauth_oidc_id`, the Pocket ID subject, never by an Mbin user id.
* **What it leaves out.** For each member, everything they wrote or commented in, voted on, boosted, bookmarked, or opened a notification about; anything from a magazine or a user they blocked or a magazine they are banned from; anything deleted, removed by a moderator or marked NSFW. A thread in a magazine the member subscribes to ranks lower, not out, in a week they visited Mbin. A member with nothing left gets no email that week.
* **Federated content.** Content that reached Mbin from other servers appears only from magazines the member subscribes to themselves, only in the compact list and never as the featured thread, and is never quoted: it shows its title, its magazine and one figure. It is weighted so that it does not crowd out local conversation.
* **Unsubscribe.** Every email carries a link signed with `ENGAGEMENT_SIGNING_KEY`. `GET` or `POST /u/{token}` unsubscribes the member at once, with no login and no confirmation, and answers one plain sentence. The same URL is in the `List-Unsubscribe` and `List-Unsubscribe-Post` headers (RFC 8058). The token has no expiry. A link that is not valid gets a 404.

A send is recorded per member per week in the service's own database before the next member starts, so a restart, a retry or a second copy never mails anyone twice. A send that fails is recorded as failed and is not retried.

The only public route is `/u/{token}`; the gateway forwards nothing else. `/healthz` answers on the listen address for the container's own health check. Without `ENGAGEMENT_SMTP_HOST` the service logs that no digest is sent and starts no schedule, and the unsubscribe route still answers.

## Mbin must be clustered

The service reads Mbin only when Mbin has `placement: cluster` in Paisans. Its read only database role is created in the cluster's Postgres, where Paisans already creates each clustered app's role, and the service reaches it through the site's HAProxy. A pinned Mbin keeps its database on its own compose network with only the role its image created, so the toolkit refuses a deployment that points the engagement blueprint at a pinned Mbin with its `engagement-source-not-clustered` check. Cluster Mbin before enabling the blueprint.

## What it reads

The service never writes to Mbin's database, and the role it uses cannot. The role is granted `SELECT` on the tables and columns below and on nothing else; `mbin.Allowlist` in `internal/mbin/allowlist.go` is the list in code, and the toolkit grants the role from a matching copy.

| Table | Columns |
|-------|---------|
| `entry` | `id`, `user_id`, `magazine_id`, `slug`, `title`, `comment_count`, `score`, `is_adult`, `visibility`, `ap_id`, `created_at`, `last_active` |
| `entry_comment` | `id`, `user_id`, `entry_id`, `magazine_id`, `body`, `up_votes`, `favourite_count`, `visibility`, `is_adult`, `created_at` |
| `post` | `id`, `user_id`, `magazine_id`, `slug`, `body`, `comment_count`, `score`, `is_adult`, `visibility`, `ap_id`, `created_at`, `last_active` |
| `post_comment` | `id`, `user_id`, `post_id`, `magazine_id`, `body`, `up_votes`, `favourite_count`, `visibility`, `is_adult`, `created_at` |
| `entry_vote` | `user_id`, `entry_id`, `choice` |
| `post_vote` | `user_id`, `post_id`, `choice` |
| `favourite` | `user_id`, `entry_id`, `post_id` |
| `bookmark` | `user_id`, `entry_id`, `post_id` |
| `notification` | `user_id`, `status`, `entry_id`, `entry_comment_id`, `post_id`, `post_comment_id` |
| `magazine` | `id`, `name`, `title`, `ap_id`, `is_adult`, `visibility`, `marked_for_deletion_at`, `ap_deleted_at` |
| `magazine_subscription` | `user_id`, `magazine_id` |
| `magazine_block` | `user_id`, `magazine_id` |
| `magazine_ban` | `user_id`, `magazine_id`, `expired_at` |
| `user_block` | `blocker_id`, `blocked_id` |
| `user` | `id`, `username`, `email`, `is_verified`, `is_banned`, `is_deleted`, `marked_for_deletion_at`, `visibility`, `type`, `ap_id`, `oauth_oidc_id`, `last_active`, `created_at` |

Mbin keeps credentials in its database, and the role never sees them. These tables are never granted: `sessions`, `rememberme_token`, `oauth2_access_token`, `oauth2_refresh_token`, `oauth2_authorization_code`, `oauth2_client`, `oauth2_user_consent`, `reset_password_request`, `user_push_subscription`, `site`, `settings`, `messenger_messages`, `message`, `message_thread`, `user_note`, `ap_activity` and `activity`. On `user`, these columns are never granted: `password`, `totp_secret`, `totp_backup_codes`, `private_key`, `old_private_key`, `ip`, and every `oauth_*_id` column except `oauth_oidc_id`. A role that could read any of them could log in as a member.

The service's own database holds two things: who unsubscribed, and which member was sent which week.

## Environment

| Variable | Meaning |
|----------|---------|
| `ENGAGEMENT_LISTEN_ADDR` | listen address, default `:8083` |
| `ENGAGEMENT_PUBLIC_URL` | `https://<engagement hostname>`, no trailing slash: the unsubscribe links in the email start with it |
| `ENGAGEMENT_DATABASE_URL` | the service's own Postgres database, through the site's HAProxy |
| `ENGAGEMENT_MBIN_DATABASE_URL` | Mbin's Postgres database, through a role that can only read |
| `ENGAGEMENT_MBIN_URL` | `https://<Mbin hostname>`, no trailing slash: every link to a thread and the button start with it |
| `ENGAGEMENT_INSTANCE_TITLE` | Mbin's site title, used on the button: `See everything on <instance title>` |
| `ENGAGEMENT_META_TITLE` | Mbin's meta title, which names the community in the subject, heading and footer |
| `ENGAGEMENT_SIGNING_KEY` | 32 bytes as unpadded base64url (43 characters): signs the unsubscribe links |
| `ENGAGEMENT_SMTP_HOST` | the mail host; empty means no digest is sent |
| `ENGAGEMENT_SMTP_PORT` | default `587` |
| `ENGAGEMENT_SMTP_USER`, `ENGAGEMENT_SMTP_PASSWORD` | mail credentials, optional |
| `ENGAGEMENT_SMTP_FROM` | the sender address, required once there is a mail host |
| `ENGAGEMENT_SMTP_FROM_NAME` | the sender's display name, optional |
| `ENGAGEMENT_SMTP_TLS` | `none`, `starttls` (the default) or `tls` |

The container's time zone sets the send time, so mount the host's `/etc/localtime` read only.

## Tests

Run the tests: `ENGAGEMENT_TEST_DATABASE_URL=postgres://… go test ./...`

Tests that need Postgres skip when the variable is unset, and each makes its own schema. The Mbin queries run against `internal/mbin/testdata/schema.sql`, a copy of the tables and columns in the allowlist.
