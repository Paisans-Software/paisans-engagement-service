# Welcoming new members, on Mbin, on Matrix and by email

Status: draft, 2026-10-29, awaiting the founder's approval of the whole.
The founder settled the scope, the agent role, operators as the alert
audience, the Matrix room's audience and the digest exclusion in session
the same day. It
extends `docs/specs/2026-10-24-engagement-digest.md` and
`docs/specs/2026-10-27-engagement-matrix.md`, which stay the description of
the weekly digest and its bot. A line marked *provisional* is the author's
call and open to correction.

## Why

A new member who signs up and hears nothing tends not to come back. On
Discord, a newcomer lands in the general channel and the server announces
them, so the people already there can say hello. Mbin and Matrix have no
such moment. This feature makes one: the community's bot announces
newcomers where members already are, and nudges the most active members by
email to go and greet them.

The bot never welcomes anyone itself and never speaks as a member. It points
existing members at newcomers. The greeting is theirs.

## What gets built

1. **The Mbin fork** (`Paisans-Software/mbin`): an agent role and two OAuth
   scopes that let a bot account subscribe members to magazines and create
   magazines (see *The agent role*).
2. **`paisans-engagement-service`**: the welcome loop for Mbin and Matrix,
   the daily welcome email, operator alerts, and a second unsubscribe list.
3. **The `engagement` blueprint** in `paisans-cli`: two new settings, the
   Mbin agent client's credentials, and the operators' email addresses.
4. **The `mbin` blueprint** in `paisans-blueprints`: registers the agent
   OAuth client and grants its account the agent role.
5. **The `synapse` blueprint** in `paisans-blueprints`: auto-joins every new
   account to the community room.

## Goals

* Every member who creates an Mbin account is announced in a thread in the
  magazine `/m/welcome`, and is subscribed to `/m/welcome`.
* Every member who creates a Matrix account is announced in the community
  room, and is in that room from the start.
* Once a day, up to a configurable number of existing members (50 by
  default) get an email naming the day's Mbin newcomers and linking their
  welcome threads.
* The Mbin welcome and the Matrix welcome are independent. A member already
  on Mbin who later creates a Matrix account is welcomed on Matrix, and the
  other way round.
* An operator hears about it when the bot cannot post.

## Non-goals

* Welcoming federated (remote) accounts.
* Emailing about Matrix-only newcomers. The email links Mbin threads, so it
  covers Mbin newcomers only.
* Blocking members' microblogs in `/m/welcome`. Mbin's mod-only setting
  blocks threads only; the fork change is tracked as
  `Paisans-Software/mbin#152` and is not part of this work.
* Any agent scope beyond the two named here, and any admin panel UI for the
  agent role.

## Anti-goals

These would make the feature a failure even if every part worked.

* **Welcoming or subscribing anyone who existed before the feature.** No
  backfill, ever. An existing member is never subscribed to `/m/welcome` and
  never gets a welcome thread or Matrix announcement for their old account.
* **The bot acting as a member.** It subscribes members to magazines and
  nothing else on their behalf. It never posts, comments, votes or boosts as
  anyone but itself.
* **Mailing a large community at once.** The daily cap holds whatever the
  size of the community.
* **Mailing anyone twice for the same day**, across restarts, retries or two
  copies of the service.
* **Writing to Mbin's database.** The service keeps reading Mbin through its
  read-only role. Every change it makes in Mbin goes through Mbin's API.
* **Taking over a room a human made.** If the community room exists and the
  bot cannot post there, it says so to an operator and changes nothing.

## Who counts as a newcomer

**On Mbin**, an account that is local, has an OIDC subject, is not a bot,
not banned, not deleted, and whose application is approved (an instance
that requires approval welcomes the member once they are approved). These
are the digest's member rules, read through the same read-only role.

**On Matrix**, a MAS user that is not deactivated, is linked to a Pocket ID
subject at the configured upstream provider, and is not the bot. These are
the Matrix digest's member rules.

**When.** An account is a newcomer when it was created after welcomes
first started **on that platform** on this deployment, and within the last
21 days, and has not been welcomed on that platform yet. Each platform has
its own start time, stored in the service's own state the first time that
platform's welcomes run and never pruned; it is what guarantees no
backfill. A service that runs Matrix welcomes for a month before Mbin's
agent client exists starts Mbin's clock only when Mbin welcomes first run,
so nobody who signed up on Mbin in between is welcomed or subscribed.
*Provisional.* A pending Mbin account approved within 21 days of
signing up is welcomed in the next batch after approval; one approved later
is not welcomed.

## Batches

Every five minutes, separately for each platform, the service collects the
newcomers not yet welcomed and, if there is at least one, makes **one**
announcement tagging all of them. An empty batch posts nothing.

Finding newcomers is a poll. On Mbin it is one query on `"user"`, newest
first by primary key, limited to accounts created in the last 21 days. On
Matrix it is one call to MAS's admin API listing users newest first (MAS
user ids are ULIDs, ordered by creation). Neither is meaningfully expensive
at a five-minute interval.

A newcomer is recorded as welcomed on a platform only after the
announcement that names them succeeds. A failed announcement is retried in
the next batch with whoever else has joined since.

## Mbin

### The magazine

At start, and before each batch if it is missing, the bot makes sure
`/m/welcome` exists:

* It creates it if absent, through the agent scope `agent:magazine:create`,
  which works even when magazine creation is restricted to admins. The bot
  is then its owner.
* It sets it posting-restricted to moderators through Mbin's existing
  magazine update API (`isPostingRestrictedToMods`), so members cannot
  start threads there. Comments stay open. It sets this every start, so a
  moderator who turns it off sees it turned back on. *Provisional.*
* Title "Welcome", description one line saying what the magazine is for.
  *Provisional* wording.

If `/m/welcome` exists and the bot is neither its owner nor a moderator, the
bot does not try to take it over: it alerts an operator (see *Operator
alerts*) and skips Mbin welcomes until it can post.

### The thread

One thread per batch in `/m/welcome`, posted as the bot through its own
OAuth token. The title names the newcomers, for example "Welcome
@alice, @bob and @carol" (more than three: "Welcome @alice, @bob and 4
others"). The body mentions every newcomer with `@username`, which Mbin turns
into notifications, and asks members to say hello in the comments.
*Provisional* wording, kept short.

### Kept out of the weekly digest

Nothing from the local `/m/welcome` appears in the weekly digest: not in
the main list, the featured thread, *Elsewhere* (Discover), the unanswered
section, the Matrix notice or the web digest. The magazine is excluded in
the digest's candidate queries. The welcome email is the only place
welcome threads are promoted.

### Subscribing newcomers

After the thread is posted, the bot subscribes each newcomer in the batch
to `/m/welcome` through `agent:magazine:subscribe`. A subscription that
fails is retried in the next batch; a newcomer already subscribed is left
alone. Nobody else is ever subscribed.

## Matrix

### The community room

**Name.** A new engagement setting, `community_room_name`. Its default is
derived from the deployment's `community.name`: the name plus " Community",
unless the name already contains, as a whole word and in any case, one of
Community, Communities, Collective, Commons, Club, Society, Guild, Circle,
Network, Group, Hub or Crew. "Riverside" gives "Riverside Community";
"Riverside Guild" stays "Riverside Guild". The toolkit computes the default
and renders the value, so the service sees one variable.

**Alias.** The room is found by an alias built from the name: lowercase,
runs of anything outside `a-z0-9` replaced by one `-`, ends trimmed.
"Riverside Community" is `#riverside-community:<server_name>`.

**Find or create.** At start the bot resolves the alias.

* Found: the bot joins it if it is not already in it.
* Not found: the bot creates it with that name and alias, joinable by
  anyone on the homeserver, not federated (`m.federate: false`), history
  visible to members, unencrypted, and published in the homeserver's room
  directory so existing members can find it. The bot holds power level 100
  as creator.

Existing members are never invited or joined. Most Matrix accounts are
expected to be new ones, which auto-join puts in the room; an existing
member joins from the directory when they choose to.

Changing `community_room_name` changes the alias, so the next start finds
or creates the room under the new name. The old room is left as it is and
the bot stops posting there.

### Auto-join

The `synapse` blueprint renders Synapse's `auto_join_rooms` with the
community room's alias whenever an engagement app with Matrix on consumes
the homeserver, so every new account is joined to the room at registration.
A rename re-renders it.

A provider's templates today see each consumer's `Name` and `Secrets` only
(`internal/render/links.go`), so the toolkit grows a way to hand a provider
a value its consumer declares: here, the engagement app's community room
alias, computed by the toolkit from `community_room_name`. The `synapse`
blueprint then requires that toolkit release. *Provisional* mechanism; the
builder may pick another that keeps the alias computed in one place. `autocreate_auto_join_rooms` is off: the bot creates
the room, not Synapse.

The builder must confirm that Synapse runs auto-join for accounts MAS
provisions (`/_synapse/mas/` provisioning) on the Synapse version the
catalog tracks. If it does not, the bot instead invites each newcomer as
part of the batch, before the announcement, and this section changes to
say so.

### The announcement

One `m.room.message` per batch in the community room, `msgtype` `m.text`,
with a plain body and an HTML body where each newcomer is a mention pill,
and `m.mentions.user_ids` listing them so they are notified. Wording like
the Mbin thread's. Sent with a transaction id derived from the batch, so a
retry is deduplicated by the homeserver.

## The daily welcome email

### When

Once a day at 09:00 in the container's local time, *provisional*. A service
that was down at 09:00 sends late the same day, never on a later day, as
the digest does.

### Who it is about

Every Mbin newcomer welcomed since the last day's email and not yet
included in one. If there are none, nobody is mailed.

### Who gets it

The cap is the engagement setting `welcome_email_daily_limit`, default 50,
a whole number of at least 1. It is split with a fixed ratio: 80% to the
most active members, rounded down, and the rest to members picked at
random. At the default that is 40 and 10.

Eligible recipients are the digest email's recipients (local, OIDC subject,
verified address, not banned or deleted, not a bot), minus anyone who
unsubscribed from the welcome email, minus the day's newcomers themselves.

**Most active** is ranked by the member's activity over the last 30 days:
threads and microblog posts written, comments written, and votes cast, on
local content, counted by the read-only role. *Provisional* measure and
window.

**Per recipient**, the email lists only the newcomers whose welcome thread
the recipient has not commented on. A recipient with nobody left is
skipped and their slot goes to the next member down the ranking (or the
next random pick). Since one thread can name several newcomers, commenting
on it counts as having greeted everyone it names.

The random share is drawn fresh each day from eligible members outside the
top share.

### What it says

Subject like "Say hello to 3 new people on <community>". One line per
newcomer: their name and a link to their welcome thread. A button to
`/m/welcome`. An unsubscribe link at the foot. *Provisional* wording. Plain
text and HTML parts, in the digest email's style.

### Unsubscribe

A separate list from the digest. The link is signed with the existing
signing key but carries a purpose, so a welcome link cannot unsubscribe
from the digest or the other way round. It behaves like the digest's
(`GET`/`POST` unsubscribes at once, `HEAD` does not, `List-Unsubscribe`
and `List-Unsubscribe-Post` headers). Unsubscribing from one list leaves
the other alone.

## Operator alerts

The operators are the deployment's declared `operators:`, the people the
Pocket ID group `operators` holds. The toolkit renders their email
addresses into the engagement `.env`, so the service needs no Pocket ID
credential.

An alert is sent when the bot cannot:

* create `/m/welcome`, post in it, or subscribe a member (for example the
  agent role is missing);
* resolve, create, join or post in the community room.

Each operator gets an email, and a Matrix direct message when MAS finds a
Matrix account linked to them. The alert names what failed and what to
check. At most one alert per kind of failure per day while it persists.

## What the service stores

The service already has its own database. This feature adds records that
only matter for a short time, and every one is deleted 21 days after it
was written:

* each newcomer welcomed, per platform, with the announcement it was in;
* each newcomer included in a day's email;
* each recipient mailed, per day (no double send);
* the last alert sent, per kind of failure.

Two things are kept indefinitely because they are state, not history: the
feature's start time, and the welcome unsubscribe list.

## The agent role (Mbin fork)

Built upstream-ready as `feat/agent_role` from `upstream-mirror`, merged
into `develop`, released as a fork version. Taking it upstream (issue,
Matrix discussion, pull request) is the founder's.

* **Role.** `ROLE_AGENT`, an account role beside `ROLE_ADMIN` and
  `ROLE_MODERATOR`. Granted and revoked only by a console command,
  `mbin:user:agent <username> [--remove]`, mirroring `mbin:user:admin`.
* **Scopes.** Two new OAuth scopes. Each endpoint requires both the role on
  the token's account and the scope on the token:
  * `agent:magazine:subscribe`: subscribe or unsubscribe a local user to a
    magazine, for example
    `PUT`/`DELETE /api/agent/users/{user_id}/magazines/{magazine_id}/subscription`.
    It goes through `MagazineManager::subscribe`/`unsubscribe`, so counts and
    events stay right.
  * `agent:magazine:create`: create a magazine even when
    `MBIN_RESTRICT_MAGAZINE_CREATION` limits creation to admins.
* **Refusals.** A private magazine (the agent cannot skip membership
  approval), a remote user, a banned or deleted user, an admin or another
  agent as the target.
* **Documentation** of the new endpoints and scopes, marked draft, per C4
  section 2.6.

Magazine housekeeping beyond creation (mod-only posting, description) uses
Mbin's existing owner and moderator API.

## Provisioning

* **The Mbin agent client.** A blueprint cannot run commands, and Mbin's
  only way to make an OAuth client (`POST /api/client`) generates the id
  and secret itself. So the client is **declared, not registered**, the way
  MAS syncs its clients from `mas.yaml`: a fork-only change,
  `fork/agent_client_bootstrap` from `develop`, has Mbin read one agent
  client from its environment at start (client id, secret, scopes, bot
  username, contact email), create or update the client and its bot
  account to match, and hold `ROLE_AGENT` on that account. Anything not
  declared there is untouched. The scopes are the ones the welcome loop
  uses: `agent:magazine:subscribe`, `agent:magazine:create`, and the
  ordinary scopes to post a thread and update a magazine it owns. This
  part is fork-only and does not go upstream; the agent role and scopes
  stay upstream-ready on their own branch. *Provisional.*
* **The secret.** Generated by the engagement app and shared with the mbin
  app, the same pattern as the MAS admin client's. That needs Mbin to be
  linked to engagement as a provider it consumes, in place of today's
  special-cased `EngagementSource()` lookup, or the existing lookup to
  carry shared secrets; the builder picks whichever is smaller in the
  toolkit. The bot's Mbin username follows `bot_localpart` where Mbin
  allows it.
* **Engagement settings.** `community_room_name` (string, one line,
  non-empty; default derived as above) and `welcome_email_daily_limit`
  (integer, at least 1, default 50). `validate` refuses anything else.
* **Engagement environment.** New variables for the Mbin API URL, the agent
  client id and secret, the community room name, the daily limit and the
  operator emails. Mbin welcomes need the agent client; Matrix welcomes
  need the Matrix variables the bot already uses; the email needs SMTP.
  Each part runs when its inputs are present and logs one line when it is
  off, as the bot does today.

## Release order

The Mbin fork release first (agent role and the declared agent client),
then the toolkit (consumer values for providers, the shared Mbin secret,
the settings and environment), then the catalog (mbin blueprint renders
the declared client, synapse auto-join, each requiring that toolkit
release), then the service. The builder confirms this against what each
release needs from the one before, as the Matrix work did.

## Left to the builder

* Exact wording of the thread, the Matrix message, the email and the
  alert, within the shapes above.
* The table layout for the 21-day records and the pruning job.
* How the batch, email and alert loops share the existing bot lock so two
  copies of the service never both post.
* The exact endpoint paths in the Mbin fork, within the shape above.
