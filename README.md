# paisans-engagement-service

paisans-engagement-service sends the community's weekly digest, by email and, on a deployment with a Matrix homeserver, as a Monday message from the Digest Bot. It is a small Go service run by the `engagement` blueprint of [Paisans](https://github.com/Paisans-Software/paisans-cli), which renders the environment below. Every Monday at 08:00 in the container's local time, it emails each member the threads and microblog posts from the past seven days that they have not joined yet: a heading with the week's dates, three figures for the whole community (new threads, comments and new members), one featured thread, a short list of more, and a button to the Mbin front page. There is no sign up and the email has no setting: the schedule is fixed, and a member who wants no more mail follows the link at the bottom of the email. The Matrix message is stopped and started by replying to the bot.

* **Who gets it.** Local Mbin accounts with an OIDC subject; the email also needs a verified address. Banned, deleted, bot and remote accounts get nothing, and neither does an address that unsubscribed. A member is identified everywhere in the service's own database and in its tokens by Mbin's `"user".oauth_oidc_id`, the Pocket ID subject, never by an Mbin user id.
* **What it leaves out.** For each member, everything they wrote or commented in, voted on, boosted, bookmarked, or opened a notification about; anything from a magazine or a user they blocked or a magazine they are banned from; anything deleted, removed by a moderator or marked NSFW. A thread in a magazine the member subscribes to ranks lower, not out, in a week they visited Mbin. A member with nothing left gets no email that week.
* **Threads it already sent.** A thread an earlier digest showed the member comes back only if it has new comments this week. It is then ranked by this week's comments and people alone, quotes the best of this week's comments, and counts "new comments this week" in place of its total. Unsubscribing forgets what a member was shown, on the terms below.
* **Federated content.** Content that reached Mbin from other servers appears only from magazines the member subscribes to themselves, only in the compact list and never as the featured thread, and is never quoted: it shows its title, its magazine and one figure. It is weighted so that it does not crowd out local conversation.
* **Unsubscribe.** Every email carries a link signed with `ENGAGEMENT_SIGNING_KEY`. `GET` or `POST /u/{token}` unsubscribes the member at once, with no login and no confirmation, and answers one plain sentence. `HEAD` answers without unsubscribing, and any other method gets a 404. The same URL is in the `List-Unsubscribe` and `List-Unsubscribe-Post` headers (RFC 8058). The token has no expiry. A link that is not valid gets a 404 and the body `This link is not valid.` The threads a member was shown are forgotten once both channels are off: unsubscribing forgets them when the member's Matrix messages are already off or the deployment has no homeserver, and stopping the Matrix messages or leaving the bot's room forgets them for a member who has already unsubscribed.

A send is recorded per member per week in the service's own database before the next member starts, so a restart, a retry or a second copy never mails anyone twice. A digest the mail relay never accepted, because the relay was down or refused the sender or the recipient, is retried the same day, with the wait between attempts doubling up to 15 minutes, and an unreachable relay stops the run until the next attempt rather than being tried once per member. A send the relay refused after the message was handed over is recorded as failed and is not retried, since it may have been delivered.

The pages are `/u/{token}` and `/d/{token}`; the gateway forwards both, and `GET /healthz`, a probe that answers ok or 503, for the monitor's public check, and nothing else. `/healthz` answers on the listen address for the container's own health check too. Without `ENGAGEMENT_SMTP_HOST` the service logs that no email is sent. The Monday run then sends only the Matrix messages when the Digest Bot is configured, and does not run when it is not; the pages answer either way.

## The Digest Bot

On a deployment with a Matrix homeserver, a member gets the same week's digest as a direct message from the Digest Bot, in the same Monday run, as well as the email.

* **Who qualifies.** A member whose Matrix account MAS links to their Pocket ID subject at the configured upstream provider (`ENGAGEMENT_MAS_UPSTREAM_PROVIDER`), and whose Matrix setting is on. The service asks MAS which user is linked to the subject and reads that user's username, so the member is `@<username>:<server name>`; it never guesses from the Mbin username. A verified email is not needed, and the email unsubscribe does not affect it. A member with no linked account is skipped and asked about again the next week.
* **The message.** An `m.notice` with the same text as an HTML `formatted_body`, so clients render the links:

  ```
  Your weekly digest from <meta title>

  • <thread title>
  • <thread title>
  • <thread title>

  Read the whole digest: <web digest link>

  Reply stop to stop these messages.
  ```

  The titles are the first three threads of that member's email, the featured one first, each linking to its thread. Microblog posts have no title and are skipped. A member with nothing new gets no message.
* **The room.** One per member, created by the bot with `preset: private_chat`, `is_direct`, the member invited, `m.federate: false`, and history visible from the invite. The member is at power level 0 and sending `m.room.encryption` takes 100, so a member cannot turn encryption on. The service checks the room after creating it and abandons one that came out encrypted. A member who has not accepted the invite keeps receiving the message in the room.
* **Commands.** Matched case insensitively, as the whole message, after trimming whitespace and surrounding punctuation:

  | What happens | Setting | Room | The bot replies |
  |--------------|---------|------|-----------------|
  | The member sends `stop` | off | kept | `Stopped. Reply start to get them again.` |
  | The member sends `start`, `resume` or `restore` | on | kept | `Started. Your next digest comes on Monday.` |
  | The member sends anything else | unchanged | kept | `Reply stop to stop these messages, or start to get them again.` |
  | The member leaves the room or rejects its invite | off | forgotten | nothing; the bot leaves too |
  | The member invites the bot to a room of their own | on | a new one | the bot rejects the invite, creates a new room, invites the member and sends `Started. Your next digest comes on Monday.` |

  Replies are `m.notice`, and the bot ignores its own messages and every notice, so two bots never loop. It reads only rooms it created and still holds a record for, and rejects an invite from anyone who is not a member. An invite whose membership check or setup fails for a reason other than an outage of the homeserver or MAS is tried for three batches and then rejected, so the member can invite again. The bot never recreates a room on its own after a member left.
* **Sending.** The same run sends the email and then the messages. A message is recorded per member per week, apart from the email, before the next member starts, and is sent with a transaction id derived from the member and the week, so a retry is never delivered twice. The bot is not a Synapse admin and is paced inside Synapse's default `rc_message` (0.2 a second, burst 10) and `rc_room_creation` (0.016 a second, burst 10); a reply to a command shares the message budget. A 429 is waited out, never counted as a failure. A homeserver that cannot be reached stops the Matrix half of the run, which retries the same day without holding up the email. A message claimed and never finished, because the service stopped in between, is claimed again after 15 minutes if the send day is not over. A refusal for one member is recorded as failed and not retried, and a vanished room is forgotten so the next week creates a new one. A token failure from the homeserver is an outage, never a member's refusal. The Matrix half ends with the send day, at local midnight; members not reached get their first message the next week.
* **Listening.** Only the copy holding the bot lock runs the bot, listener and messages both. It keeps a `/sync` long poll open and stores the sync token, so a restart neither replays old commands nor loses new ones. A copy whose bot is still starting makes the run retry the same day rather than skip the messages, and a copy that takes the bot over later in the send day sends the messages that are still unsent.
* **The account.** The localpart comes from `ENGAGEMENT_MATRIX_BOT_LOCALPART` (the engagement blueprint's `bot_localpart` setting) and the display name is `Digest Bot`. The service creates the account through MAS and sets the name through the client API. If the name is taken by an account it did not create, the bot does not start and the log names the variable and the `bot_localpart` setting. Its access token is a MAS personal session scoped `urn:matrix:client:api:* urn:matrix:client:device:DIGESTBOT`; it is not a Synapse admin. The service needs MAS 1.5.0 or later and checks the version at start; an older or unreadable version turns Matrix off and the log names it.

## The web digest

Each sent digest can be read at `/d/{token}` on the engagement hostname, with no login. The page is the digest as it was sent that week, stored at send time and not recomputed, so it always agrees with the message's three titles. It has the email's content without the unsubscribe link.

The token names the member and the week and is signed with `ENGAGEMENT_SIGNING_KEY`, with a purpose that differs from the unsubscribe token, so neither is accepted as the other. The digest expires 28 days after it was sent, and the stored copy is deleted then. Only `GET` and `HEAD` are answered, and opening a digest records nothing. The page carries `Referrer-Policy: no-referrer`, `X-Robots-Tag: noindex` and `Cache-Control: private, no-store`. A token that is bad, unknown or expired gets the same 404 and the same body as a bad unsubscribe link.

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

The service's own database holds who unsubscribed, which member was emailed which week, which threads each sent digest showed, each member's Matrix setting, account and room, which member was sent a message which week, each member's digest for the week, as built for that week's sends (deleted at expiry), and the bot's own state: its sync token, access token and MAS ids.

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
| `ENGAGEMENT_SMTP_HOST` | the mail host; empty means no digest email is sent |
| `ENGAGEMENT_SMTP_PORT` | default `587` |
| `ENGAGEMENT_SMTP_USER`, `ENGAGEMENT_SMTP_PASSWORD` | mail credentials, optional |
| `ENGAGEMENT_SMTP_FROM` | the sender address, required once there is a mail host |
| `ENGAGEMENT_SMTP_FROM_NAME` | the sender's display name, optional |
| `ENGAGEMENT_SMTP_TLS` | `none`, `starttls` (the default) or `tls` |
| `ENGAGEMENT_MATRIX_URL` | the homeserver's `base_url`, where the bot reaches the client API |
| `ENGAGEMENT_MATRIX_SERVER_NAME` | the homeserver's `server_name`, the name in every Matrix id |
| `ENGAGEMENT_MAS_ADMIN_URL` | MAS's admin listener, reached over the mesh |
| `ENGAGEMENT_MAS_CLIENT_ID`, `ENGAGEMENT_MAS_CLIENT_SECRET` | the MAS admin client, shared with the synapse app |
| `ENGAGEMENT_MAS_UPSTREAM_PROVIDER` | the id of the upstream provider members are linked through |
| `ENGAGEMENT_MATRIX_BOT_LOCALPART` | the bot's localpart, from the `bot_localpart` setting, `digest-bot` by default |

The bot runs only when all seven Matrix variables are set; otherwise one log line says Matrix is off and the service sends the email alone.

The container's time zone sets the send time, so mount the host's `/etc/localtime` read only.

## Tests

Run the tests: `ENGAGEMENT_TEST_DATABASE_URL=postgres://… go test ./...`

Tests that need Postgres skip when the variable is unset, and each makes its own schema. The Mbin queries run against `internal/mbin/testdata/schema.sql`, a copy of the tables and columns in the allowlist.
