# paisans-engagement-service

paisans-engagement-service sends the community's weekly digest, by email and, on a deployment with a Matrix homeserver, as a Monday message from a Matrix bot, Community Bot by default. It is a small Go service run by the `engagement` blueprint of [Paisans](https://github.com/Paisans-Software/paisans-cli), which renders the environment below. Every Monday at 08:00 in the container's local time, it emails each member the threads and microblog posts from the past seven days that they have not joined yet: a heading with the week's dates, three figures for the whole community (new threads, comments and new members), one featured thread, a short list of more, a few new threads nobody has answered yet, and a button to the Mbin front page. Each thread shows its title as a link and its magazine. A microblog post has no title, so the email quotes up to 200 characters of it and links its magazine line to it. The featured thread also quotes the best liked comment on it. There is no sign up and the email has no setting: the schedule is fixed, and a member who wants no more mail follows the link at the bottom of the email. The Matrix message is stopped and started by replying to the bot.

* **Who gets it.** Local Mbin accounts with an OIDC subject; the email also needs a verified address. Banned, deleted, bot and remote accounts get nothing, and neither does an address that unsubscribed. A member is identified everywhere in the service's own database, in its unsubscribe tokens and by the sign-in gate by Mbin's `"user".oauth_oidc_id`, the Pocket ID subject, never by an Mbin user id.
* **What it leaves out.** For each member, everything they wrote or commented in, voted on, boosted, bookmarked, or opened a notification about; anything from a magazine or a user they blocked or a magazine they are banned from; anything deleted, removed by a moderator or marked NSFW. A thread in a magazine the member subscribes to ranks lower, not out, in a week they visited Mbin. A member with nothing left gets no email that week.
* **Threads it already sent.** A thread an earlier digest showed the member comes back only if it has new comments this week. It is then ranked by this week's comments and people alone, and quotes the best of this week's comments. Unsubscribing forgets what a member was shown, on the terms below.
* **New threads nobody has answered.** Under a heading picked at random for each email, "New posts", "No replies yet" or "Start the conversation", the email offers up to three of the week's new local threads and microblog posts that nobody but their author has commented on, voted on, boosted or favourited, picked at random afresh for each member. The same rules leave things out as for the rest of the digest, and so do the member's own posts, anything shown elsewhere in the same email and anything an earlier digest showed. The section is left out when nothing is left, and it never makes an email on its own.
* **Federated content.** Content that reached Mbin from other servers appears only from magazines the member subscribes to themselves, only in the compact list and never as the featured thread, and is never quoted. It is weighted so that it does not crowd out local conversation.
* **Unsubscribe.** Every email carries a link signed with `ENGAGEMENT_SIGNING_KEY`. `GET` or `POST /u/{token}` unsubscribes the member at once, with no login and no confirmation, and answers one plain sentence. `HEAD` answers without unsubscribing, and any other method gets a 404. The same URL is in the `List-Unsubscribe` and `List-Unsubscribe-Post` headers (RFC 8058). The token has no expiry. A link that is not valid gets a 404 and the body `This link is not valid.` The threads a member was shown are forgotten once both channels are off: unsubscribing forgets them when the member's Matrix messages are already off or the deployment has no homeserver, and stopping the Matrix messages or leaving the bot's room forgets them for a member who has already unsubscribed.

A send is recorded per member per week in the service's own database before the next member starts, so a restart, a retry or a second copy never mails anyone twice. A digest the mail relay never accepted, because the relay was down or refused the sender or the recipient, is retried the same day, with the wait between attempts doubling up to 15 minutes, and an unreachable relay stops the run until the next attempt rather than being tried once per member. A send the relay refused after the message was handed over is recorded as failed and is not retried, since it may have been delivered.

The pages are `/u/{token}`, `/d/{week}` and `/d/preview/{week}`, and `GET /healthz`, a probe that answers ok or 503, for the monitor's public check, and nothing else. The engagement hostname sits behind the community's sign-in gate, which leaves only `/u/` and `/healthz` open, so an unsubscribe link in an inbox works without signing in. `/healthz` answers on the listen address for the container's own health check too. Without `ENGAGEMENT_SMTP_HOST` the service logs that no email is sent. The Monday run then sends only the Matrix messages when the Matrix bot is configured, and does not run when it is not; the pages answer either way.

## The Matrix bot

On a deployment with a Matrix homeserver, a member gets the same week's digest as a direct message from the bot, in the same Monday run, as well as the email.

* **Who qualifies.** A member whose Matrix account MAS links to their Pocket ID subject at the configured upstream provider (`ENGAGEMENT_MAS_UPSTREAM_PROVIDER`), and whose Matrix setting is on. The service asks MAS which user is linked to the subject and reads that user's username, so the member is `@<username>:<server name>`; it never guesses from the Mbin username. A verified email is not needed, and the email unsubscribe does not affect it. A member with no linked account is skipped and asked about again the next week.
* **The message.** An `m.notice` with the same text as an HTML `formatted_body`, so clients render the links. In the HTML the line reads "Read the whole digest." with "Read" as the link, and the blank lines are `<br>`s so they show in every client:

  ```
  Your weekly digest from <meta title>

  • <thread title>
  • "<microblog post>"
  • <thread title>

  Read the whole digest: <web digest link>

  Reply 'stop' to stop these messages.
  ```

  The lines are the first five threads and microblog posts of that member's email, the featured one first, each linking to its thread or post. A microblog post has no title, so its text is quoted, as in the email. A member with nothing new gets no message.
* **The room.** One per member, created by the bot with `preset: private_chat`, `is_direct`, the member invited, `m.federate: false`, and history visible from the invite. The member is at power level 0 and sending `m.room.encryption` takes 100, so a member cannot turn encryption on. The service checks the room after creating it and abandons one that came out encrypted. A member who has not accepted the invite keeps receiving the message in the room.
* **Commands.** Matched case insensitively, as the whole message, after trimming whitespace and surrounding punctuation, so `'stop'`, `‘stop’` and `"stop"` are `stop` too:

  | What happens | Setting | Room | The bot replies |
  |--------------|---------|------|-----------------|
  | The member sends `stop` | off | kept | `Stopped. Reply start to get them again.` |
  | The member sends `start`, `resume` or `restore` | on | kept | `Started. Your next digest comes on Monday.` |
  | The member sends anything else | unchanged | kept | `Reply 'stop' to stop these messages, or start to get them again.` |
  | The member leaves the room or rejects its invite | off | forgotten | nothing; the bot leaves too |
  | The member invites the bot to a room of their own | on | a new one | the bot rejects the invite, creates a new room, invites the member and sends `Started. Your next digest comes on Monday.` |

  Replies are `m.notice`, and the bot ignores its own messages and every notice, so two bots never loop. It reads only rooms it created and still holds a record for, and rejects an invite from anyone who is not a member. An invite whose membership check or setup fails for a reason other than an outage of the homeserver or MAS is tried for three batches and then rejected, so the member can invite again. The bot never recreates a room on its own after a member left.
* **Sending.** The same run sends the email and then the messages. A message is recorded per member per week, apart from the email, before the next member starts, and is sent with a transaction id derived from the member and the week, so a retry is never delivered twice. The bot is not a Synapse admin and is paced inside Synapse's default `rc_message` (0.2 a second, burst 10) and `rc_room_creation` (0.016 a second, burst 10); a reply to a command shares the message budget. A 429 is waited out, never counted as a failure. A homeserver that cannot be reached stops the Matrix half of the run, which retries the same day without holding up the email. A message claimed and never finished, because the service stopped in between, is claimed again after 15 minutes if the send day is not over. A refusal for one member is recorded as failed and not retried, and a vanished room is forgotten so the next week creates a new one. A token failure from the homeserver is an outage, never a member's refusal. The Matrix half ends with the send day, at local midnight; members not reached get their first message the next week.
* **Listening.** Only the copy holding the bot lock runs the bot, listener and messages both; `send --matrix` by hand is the one exception, described under Sending by hand. It keeps a `/sync` long poll open and stores the sync token, so a restart neither replays old commands nor loses new ones. A copy whose bot is still starting makes the run retry the same day rather than skip the messages, and a copy that takes the bot over later in the send day sends the messages that are still unsent.
* **The account.** The localpart comes from `ENGAGEMENT_MATRIX_BOT_LOCALPART` (the engagement blueprint's `bot_localpart` setting) and the display name from `ENGAGEMENT_MATRIX_BOT_DISPLAY_NAME` (its `bot_display_name` setting), `Community Bot` when unset. The service creates the account through MAS, and at every start sets the display name through the client API when the profile has a different one, so changing the setting renames the bot at the next start. If the localpart is taken by an account it did not create, the bot does not start and the log names the variable and the `bot_localpart` setting. A new localpart makes a new bot: at the next start the service creates or claims that account and forgets the old one's token, `/sync` position and every member's room, so the next send invites each member to a new room from the new bot. Members' stop and start settings and the week's sends are kept. The old account and its rooms are left as they are, and the service never posts as it again. Its access token is a MAS personal session scoped `urn:matrix:client:api:* urn:matrix:client:device:DIGESTBOT`; it is not a Synapse admin. The service needs MAS 1.5.0 or later and checks the version at start; an older or unreadable version turns Matrix off and the log names it.

## The web digest

Each sent digest can be read at `/d/{week}` on the engagement hostname, where `{week}` is the week's Monday as `YYYY-MM-DD`, by the member it was sent to. The page is the digest as it was sent that week, stored at send time and not recomputed, so it always agrees with the message's lines. It has the email's content without the unsubscribe link. The link carries no member and no signature, so it is the same for every member that week.

The page relies on the community's sign-in gate (oauth2-proxy behind Caddy's `forward_auth`) in front of the engagement hostname. For a request the gate admits, Caddy passes `X-Auth-Request-User`, the member's Pocket ID subject, and strips any copy a client sends; the page shows that member's own digest for the week and no one else's. A request with no `X-Auth-Request-User` means the hostname is not gated: it gets the same 404 and body as a bad link, whether or not a digest exists, and the service logs a warning for each one.

The digest expires 28 days after it was sent, and the stored copy is deleted then. Only `GET` and `HEAD` are answered, and opening a digest records nothing. The page carries `Referrer-Policy: no-referrer`, `X-Robots-Tag: noindex` and `Cache-Control: private, no-store`. A week that is not a date, unknown or expired gets the same 404 and the same body as a bad unsubscribe link. `/d/preview/{week}` opens the member's Matrix preview copy of that week's digest, for 24 hours.

## Sending by hand

`send` runs one digest now, by email or with `--matrix` as a Matrix message, and exits, for a test or for a Monday the service missed. It covers the week that ended at the latest Monday 08:00 in the container's local time, on whatever day it runs, and starts no server and no schedule. From the app's directory on a host that runs it:

```sh
docker compose exec app /paisans-engagement-service send --member <username>
docker compose exec app /paisans-engagement-service send --member <username> --preview
docker compose exec app /paisans-engagement-service send
docker compose exec app /paisans-engagement-service send --member <username> --matrix
docker compose exec app /paisans-engagement-service send --member <username> --matrix --preview
```

The image has no shell, so `exec` names the binary. `docker compose run --rm app send --member <username>` works too, in a container of its own, since the binary is the image's entrypoint.

* **Never twice.** `send` claims each member for the week exactly as the schedule does, so a member the schedule or an earlier `send` already reached that week is skipped. The next Monday is a new week and sends as usual.
* **One member.** `--member` takes an Mbin username and sends to that member alone. Nobody else is claimed, so a later `send` without it still reaches the rest of the community for the same week. A username that gets no digest is refused and nothing is sent.
* **A preview.** `--preview` needs `--member`: `send --member alice --preview` mails alice the digest a brand-new member would get this week, with nothing joined, blocked or followed and nothing shown before, so a tester who has already joined everything still sees a full one, and alice's own new threads may be among the unanswered ones. No member's own activity is read. The subject starts `[Preview]`, and the unsubscribe link is alice's own. A preview claims nobody, records nothing as shown and stores no digest of the week, so the schedule or a later `send` still reaches alice with her own. It is refused, and nothing is sent, when alice unsubscribed, has no verified address, or when the week has nothing for even a new member.
* **The Matrix message.** `--matrix` needs `--member` and sends the bot's message instead of the email, through the same pacing and 429 handling as the schedule. `send --member alice --matrix` sends alice her message for the week now, exactly as the Monday run would for her alone, on any day: it is claimed and recorded the same way, it uses the week's transaction id, and it is skipped if the week's message already reached her. No email is sent and nobody else is claimed.
* **A Matrix preview.** `send --member alice --matrix --preview` sends alice the Monday message built from the same brand-new member's digest as the email preview, starting `[Preview]`, into her room with the bot, which is created if she has none. It has a transaction id of its own each time, so a second preview arrives too. Its link opens a copy of that digest for 24 hours; the copy is stored apart from the week's digests and has its own path, `/d/preview/{week}`, so it never opens, satisfies or blocks alice's own digest for the week. Nothing is claimed and nothing is recorded as shown, so the week's Matrix message still reaches her.
* **The bot from `send`.** When no copy of the service runs the bot, `send --matrix` takes the bot lock, starts the bot as the service does and releases the lock when the send is over. When the running service holds the lock, `send` acts as that bot with the access token the service stored: it creates no account or token, never renews one, and leaves the service's lock and `/sync` listener alone; the listener ignores the bot's own messages. A token the homeserver refuses is replaced only by a newer one the service has stored, and otherwise the send fails and can be run again.
* **Refusals.** `--matrix` is refused, and nothing is sent, when any of the seven Matrix variables is unset, before anything is opened; when the member is unknown, has stopped the bot, or has no Matrix account linked through MAS; when the bot cannot start, for example because its localpart is taken; and, for a preview, when the week has nothing for even a new member. `--matrix` needs no mail host.
* **The schedule does not catch up.** The schedule sends only on the Monday itself, so after a test with `--member` on a later day the rest of that week goes out only if `send` is run again without it.
* **Exit status.** `send` logs the same summary line as a scheduled run and exits 0, or logs the error and exits 1. Without `ENGAGEMENT_SMTP_HOST` an email send refuses before reading anything. A member whose digest the relay or the homeserver never accepted is left unclaimed and the exit is 1, so running `send` again reaches them.

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

The service's own database holds who unsubscribed, which member was emailed which week, which threads each sent digest showed, each member's Matrix setting, account and room, which member was sent a message which week, each member's digest for the week, as built for that week's sends (deleted at expiry), the latest Matrix preview of a week's digest per member (deleted after 24 hours), and the bot's own state: its sync token, access token and MAS ids.

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
| `ENGAGEMENT_MATRIX_BOT_LOCALPART` | the bot's localpart, from the `bot_localpart` setting, `community-bot` by default |
| `ENGAGEMENT_MATRIX_BOT_DISPLAY_NAME` | the bot's display name, from the `bot_display_name` setting, optional; `Community Bot` when unset or empty, at most 256 characters, one line |

The bot runs only when all seven Matrix variables are set (the display name is not one of them); otherwise one log line says Matrix is off and the service sends the email alone.

The container's time zone sets the send time, so mount the host's `/etc/localtime` read only.

## Tests

Run the tests: `ENGAGEMENT_TEST_DATABASE_URL=postgres://… go test ./...`

Tests that need Postgres skip when the variable is unset, and each makes its own schema. The Mbin queries run against `internal/mbin/testdata/schema.sql`, a copy of the tables and columns in the allowlist.
