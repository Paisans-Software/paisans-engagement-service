# paisans-engagement-service

paisans-engagement-service sends the community's weekly digest email, a small Go service run by the `engagement` blueprint of [Paisans](https://github.com/Paisans-Software/paisans-cli), which renders the environment below. Every Monday at 08:00 in the container's local time, it emails each member the threads and microblog posts from the past seven days that they have not joined yet: a heading with the week's dates, three figures for the whole community (new threads, comments and new members), one featured thread, a short list of more, and a button to the Mbin front page. There is no sign up and no setting: the schedule is fixed, and a member who wants no more mail follows the link at the bottom of the email.

* **Who gets it.** Local Mbin accounts with a verified email address and an OIDC subject. Banned, deleted, unverified, bot and remote accounts get nothing, and neither does an address that unsubscribed. A member is identified everywhere in the service's own database and in its tokens by Mbin's `"user".oauth_oidc_id`, the Pocket ID subject, never by an Mbin user id.
* **What it leaves out.** For each member, everything they wrote or commented in, voted on, boosted, bookmarked, or opened a notification about; anything from a magazine or a user they blocked or a magazine they are banned from; anything deleted, removed by a moderator or marked NSFW. A thread in a magazine the member subscribes to ranks lower, not out, in a week they visited Mbin. A member with nothing left gets no email that week.
* **Threads it already sent.** A thread an earlier digest showed the member comes back only if it has new comments this week. It is then ranked by this week's comments and people alone, quotes the best of this week's comments, and counts "new comments this week" in place of its total. Unsubscribing forgets what a member was shown.
* **Federated content.** Content that reached Mbin from other servers appears only from magazines the member subscribes to themselves, only in the compact list and never as the featured thread, and is never quoted: it shows its title, its magazine and one figure. It is weighted so that it does not crowd out local conversation.
* **Unsubscribe.** Every email carries a link signed with `ENGAGEMENT_SIGNING_KEY`. `GET` or `POST /u/{token}` unsubscribes the member at once, with no login and no confirmation, and answers one plain sentence. `HEAD` answers without unsubscribing, and any other method gets a 404. The same URL is in the `List-Unsubscribe` and `List-Unsubscribe-Post` headers (RFC 8058). The token has no expiry. A link that is not valid gets a 404.

A send is recorded per member per week in the service's own database before the next member starts, so a restart, a retry or a second copy never mails anyone twice. A digest the mail relay never accepted, because the relay was down or refused the sender or the recipient, is retried the same day, with the wait between attempts doubling up to 15 minutes, and an unreachable relay stops the run until the next attempt rather than being tried once per member. A send the relay refused after the message was handed over is recorded as failed and is not retried, since it may have been delivered.

The only page is `/u/{token}`; the gateway also forwards `GET /healthz`, a probe that answers ok or 503, for the monitor's public check, and nothing else. `/healthz` answers on the listen address for the container's own health check too. Without `ENGAGEMENT_SMTP_HOST` the service logs that no digest is sent and starts no schedule, and the unsubscribe route still answers.

## Sending by hand

`send` runs one digest now and exits, for a test or for a Monday the service missed. It covers the week that ended at the latest Monday 08:00 in the container's local time, on whatever day it runs, and starts no server and no schedule. From the app's directory on a host that runs it:

```sh
docker compose exec app /paisans-engagement-service send --member <username>
docker compose exec app /paisans-engagement-service send
```

The image has no shell, so `exec` names the binary. `docker compose run --rm app send --member <username>` works too, in a container of its own, since the binary is the image's entrypoint.

* **Never twice.** `send` claims each member for the week exactly as the schedule does, so a member the schedule or an earlier `send` already reached that week is skipped. The next Monday is a new week and sends as usual.
* **One member.** `--member` takes an Mbin username and sends to that member alone. Nobody else is claimed, so a later `send` without it still reaches the rest of the community for the same week. A username that gets no digest is refused and nothing is sent.
* **The schedule does not catch up.** The schedule sends only on the Monday itself, so after a test with `--member` on a later day the rest of that week goes out only if `send` is run again without it.
* **Exit status.** `send` logs the same summary line as a scheduled run and exits 0, or logs the error and exits 1. Without `ENGAGEMENT_SMTP_HOST` it refuses before reading anything. A member whose digest the relay never accepted is left unclaimed and the exit is 1, so running `send` again reaches them.

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

The service's own database holds three things: who unsubscribed, which member was sent which week, and which threads each sent digest showed.

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
