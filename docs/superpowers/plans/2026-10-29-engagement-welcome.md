# Welcoming new members: service implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Announce new Mbin members in a `/m/welcome` thread and new Matrix members in the community room, every five minutes, and once a day email up to `ENGAGEMENT_WELCOME_DAILY_LIMIT` existing members asking them to greet the day's Mbin newcomers.

**Architecture:** A new `internal/welcome` package holds the loops (Mbin batch, Matrix batch, daily email, alerts, pruning) behind small interfaces; it never talks HTTP or SQL itself. `internal/mbinapi` is a new client for Mbin's HTTP API as the agent bot. `internal/mbin` gains read queries for newcomers, ranking and comments; `internal/mas`, `internal/matrix` and `internal/bot` gain the newest-users list, alias, join and community-room calls. The service's own database gains short-lived welcome records pruned after 21 days. Everything runs only on the copy holding the existing bot lock.

**Tech Stack:** Go, pgx/Postgres, `html/template`/`text/template`, `net/http`.

**Spec:** `docs/superpowers/specs/2026-10-29-engagement-welcome-design.md`. Cross-repo names: paisans-cli `docs/superpowers/plans/2026-10-29-engagement-welcome-contract.md`.

## Global Constraints

- Environment names exactly as the contract: `ENGAGEMENT_MBIN_API_URL`, `ENGAGEMENT_MBIN_AGENT_CLIENT_ID`, `ENGAGEMENT_MBIN_AGENT_CLIENT_SECRET`, `ENGAGEMENT_COMMUNITY_ROOM_NAME`, `ENGAGEMENT_COMMUNITY_ROOM_ALIAS`, `ENGAGEMENT_WELCOME_DAILY_LIMIT` (default 50, at least 1), `ENGAGEMENT_OPERATOR_EMAILS` (comma-separated).
- Mbin agent scopes requested: `agent:magazine:subscribe agent:magazine:create read write moderate:magazine_admin:update`.
- Magazine name `welcome`. Batch interval 5 minutes. Records pruned after 21 days. Email at 09:00 local, same day only. Split: top `limit*80/100` (integer division), random the rest. Activity window 30 days.
- The service never writes to Mbin's database. Mbin reads stay inside `mbin.Allowlist`; this plan adds `user.application_status`, `entry_vote.created_at`, `post_vote.created_at`, which the toolkit's copy (`internal/apply/sourcegrant.go`) must match.
- Each part runs only when its inputs are set, and logs one line naming what is missing when it does not.
- Before PR: `gofmt -l .` empty, `go vet ./...` clean, `go test ./...` with `ENGAGEMENT_TEST_DATABASE_URL=postgres://postgres:test@localhost:55434/engagement_test?sslmode=disable`.
- CHANGELOG bullets under `## [Unreleased]`, at most 35 words each. README describes current state only.

## Review Focus

1. A restart in the middle of the day's email must not mail anyone twice or exceed the cap. Task 9 pins it (`TestEmailRestartKeepsCap`).
2. A member created before welcomes first ran on a platform is never welcomed or subscribed, even when the other platform started earlier. Task 3 and Task 9 pin it (`TestStartedPerPlatform`, `TestMbinBatchSkipsMembersBeforeStart`).
3. An existing `/m/welcome` the bot cannot manage, or a community room it cannot post in, sends one operator alert per kind per day and changes nothing. Task 9 pins it (`TestAlertOncePerDay`, `TestMbinBatchAlertsWhenNotModerator`).
4. A welcome unsubscribe link never unsubscribes from the digest, and a digest link never from the welcome email. Task 1 and Task 10 pin it.
5. A username or room name with HTML in it must not inject markup into the Matrix message or the email. Task 8 pins it (`TestAnnouncementEscapes`).

---

### Task 1: Unsubscribe tokens carry a purpose

**Files:** Modify `internal/token/token.go`; Test `internal/token/token_test.go`.

**Interfaces:** Produces `type Purpose string`, `const Digest Purpose = "digest"`, `const Welcome Purpose = "welcome"`, `SignFor(key []byte, p Purpose, subject string) string`, `VerifyFor(key []byte, tok string) (p Purpose, subject string, ok bool)`. `Sign`/`Verify` keep meaning `Digest`, byte for byte as before.

- [ ] Test `TestWelcomeTokenIsNotADigestToken`: `Verify(key, SignFor(key, Welcome, "s"))` is false; `VerifyFor` of it gives `Welcome, "s", true`; `VerifyFor(key, Sign(key, "s"))` gives `Digest`. Run, see it fail, implement (welcome tokens are `w.` + the digest token shape with `"unsubscribe-welcome\x00"` in the MAC), run, commit.

### Task 2: Configuration

**Files:** Modify `internal/config/config.go`; Test `internal/config/config_test.go`.

**Interfaces:** Produces `type Welcome struct { MbinAPIURL, AgentClientID, AgentClientSecret, RoomName, RoomAlias string; DailyLimit int; Operators []string }` as `Config.Welcome`; `func (w Welcome) MbinMissing() []string` (the three agent variables), `func (w Welcome) RoomMissing() []string` (the two room variables).

- [ ] Tests: defaults (`DailyLimit == 50`, empty parts report missing names); `ENGAGEMENT_WELCOME_DAILY_LIMIT=0` and `abc` are errors; `ENGAGEMENT_OPERATOR_EMAILS=" a@x , b@y "` gives `["a@x","b@y"]`; `ENGAGEMENT_MBIN_API_URL` with a trailing slash is an error; a room name with a newline is an error; an alias outside `^[a-z0-9-]+$` is an error. Implement, run, commit.

### Task 3: Welcome records in the service database

**Files:** Create `internal/store/migrations/0006_welcome.sql`, `internal/store/welcome.go`; Test `internal/store/welcome_test.go`.

**Interfaces:** Produces:
- `type Platform string` with `PlatformMbin = "mbin"`, `PlatformMatrix = "matrix"`.
- `WelcomeStart(ctx, p Platform, now time.Time) (time.Time, error)`: the stored start, storing `now` the first time.
- `type Welcomed struct { Platform Platform; Account, Name, URL string; MbinID, EntryID int64; Subscribed bool; At time.Time }`.
- `WelcomedAccounts(ctx, p) (map[string]bool, error)`; `RecordWelcomed(ctx, []Welcomed) error` (one transaction, conflicts ignored); `WelcomeSubscribed(ctx, account string) error` and `PendingSubscriptions(ctx) ([]Welcomed, error)` for Mbin rows not yet subscribed.
- `ClaimWelcomesForDay(ctx, day time.Time) ([]Welcomed, error)`: marks every Mbin row with an entry and no day as the day's, then returns the day's rows.
- `ClaimWelcomeSend(ctx, subject string, day time.Time) (bool, error)`, `FinishWelcomeSend(ctx, subject, day, status Status) error`, `ReleaseWelcomeSend(ctx, subject, day) error`, `WelcomeSendsOn(ctx, day) (map[string]bool, error)`.
- `UnsubscribeWelcome(ctx, subject) error`, `WelcomeUnsubscribed(ctx) (map[string]bool, error)`.
- `AlertDue(ctx, kind string, now time.Time) (bool, error)`: true at most once per kind per calendar day.
- `PruneWelcome(ctx, now time.Time) error`: deletes welcome rows, sends and alerts older than 21 days. Starts and unsubscribes are kept.

- [ ] Tests (DB): `TestStartedPerPlatform` (first call stores, second returns the first, platforms independent); `TestClaimWelcomesForDayIsStable` (two calls the same day return the same rows; a row recorded later goes to the next day); `TestWelcomeSendClaimOnce`; `TestAlertDueOncePerDay`; `TestPruneWelcomeKeepsStartsAndUnsubscribes`. Implement, run, commit.

### Task 4: Mbin reads

**Files:** Modify `internal/mbin/queries.go`, `internal/mbin/mbin.go`, `internal/mbin/allowlist.go`, `internal/mbin/testdata/schema.sql`; Test `internal/mbin/welcome_test.go`, `internal/mbin/mbin_test.go`.

**Interfaces:** Produces on `*mbin.Source`:
- `Newcomers(ctx, since time.Time) ([]Newcomer, error)`, `type Newcomer struct { ID int64; Subject, Username string; CreatedAt time.Time }`: member rule plus `application_status = 'Approved'`, created at or after `since`, newest first by id, at most 500.
- `Active(ctx, since time.Time) ([]Ranked, error)`, `type Ranked struct { digest.Member; Score int }`: every member with a verified address, ordered by local threads, microblogs, comments and votes since `since`, then by id.
- `Commenters(ctx, entryIDs []int64) (map[int64]map[int64]bool, error)`: entry id → member ids with a visible comment.
- `SubjectsByEmail(ctx, emails []string) (map[string]string, error)`: lower-cased email → subject, members only.
- The digest leaves out the local magazine `welcome` everywhere: `hotSQL`, `unansweredSQL` and the `following` rows of `choicesSQL`.

- [ ] Tests (DB): `TestNewcomersApprovedLocalSince` (pending, remote, bot, banned, older rows absent); `TestActiveRanksByActivity`; `TestCommenters`; `TestSubjectsByEmail`; `TestDigestLeavesOutWelcome` (a busy `welcome` thread is absent from `Hot`, `Unanswered` and `Following`; a remote magazine named `welcome` is not). Implement, run, commit.

### Task 5: Mbin API client

**Files:** Create `internal/mbinapi/client.go`; Test `internal/mbinapi/client_test.go`.

**Interfaces:** Produces `New(baseURL, clientID, secret string, hc *http.Client) *Client`; `Scopes` constant (Global Constraints); `type Magazine struct { ID int64; Name string; PostingRestricted bool }`; `type Entry struct { ID int64; Slug string }`; methods `MagazineByName(ctx, name) (Magazine, bool, error)`, `CreateMagazine(ctx, name, title, description string) (Magazine, error)` (`POST /api/agent/magazines`), `RestrictPosting(ctx, id int64) error` (`PUT /api/moderate/magazine/{id}`, body `{"isPostingRestrictedToMods":true,"isAdult":false}`), `CreateThread(ctx, magazineID int64, title, body string) (Entry, error)` (`POST /api/magazine/{id}/article`, body `title`, `body`, `lang` `en`), `Subscribe(ctx, userID, magazineID int64) error` (`PUT /api/agent/users/{u}/magazines/{m}/subscription`). Errors: `ErrForbidden` for 401/403 after one token refresh, `ErrRefused` for 400, `ErrUnavailable` for transport errors and 5xx.

- [ ] Tests against `httptest`: token request form carries the grant, id, secret and scopes, and is cached; a 401 drops the token and retries once; each method's path, method and JSON; 403 → `ErrForbidden`; 400 → `ErrRefused`; 503 → `ErrUnavailable`; 404 on `MagazineByName` → not found. Implement, run, commit.

### Task 6: MAS newest users, Matrix rooms

**Files:** Modify `internal/mas/client.go`, `internal/mas/mastest/server.go`, `internal/matrix/rooms.go`, `internal/matrix/matrixtest/server.go`; Test `internal/mas/client_test.go`, `internal/matrix/rooms_test.go`.

**Interfaces:** Produces `(*mas.Client).NewestUsers(ctx, n int) ([]mas.User, error)` (`GET /api/admin/v1/users?page[last]=n&filter[status]=active`); `(*matrix.Client).ResolveAlias(ctx, alias string) (roomID string, found bool, err error)`; `(*matrix.Client).Join(ctx, roomIDOrAlias string) (string, error)`; `(*matrix.Client).CreateCommunityRoom(ctx, name, aliasLocalpart string) (string, error)` (preset `public_chat`, `visibility` `public`, `room_alias_name`, `name`, `m.federate` false, `history_visibility` `shared`). Fakes gain the list, alias directory and join.

- [ ] Tests: newest users query and order; resolve found / `M_NOT_FOUND`; create request body fields; join returns the room id. Implement, run, commit.

### Task 7: Bot calls for welcomes

**Files:** Create `internal/bot/welcome.go`; Test `internal/bot/welcome_test.go`.

**Interfaces:** Produces `type Newcomer struct { MASUserID, UserID, Username string; CreatedAt time.Time }`; `(*Bot).NewMembers(ctx, since time.Time) ([]Newcomer, error)` (newest 200 MAS users created at or after `since`, not the bot, not deactivated or locked, linked at the configured provider); `(*Bot).CommunityRoom(ctx, name, alias string) (string, error)` (resolve, join if found, create if not); `(*Bot).Announce(ctx, roomID, txnID string, content map[string]any) error`. All go through `b.call` for token renewal.

- [ ] Tests with `matrixtest`/`mastest`: unlinked, deactivated, older and bot accounts absent; existing alias is joined not created; missing alias is created with the name; `Announce` refused → error matching `refused`. Implement, run, commit.

### Task 8: Wording

**Files:** Create `internal/welcome/words.go`; Test `internal/welcome/words_test.go`.

**Interfaces:** Produces `ThreadTitle(usernames []string) string` ("Welcome @a", "Welcome @a and @b", "Welcome @a, @b and @c", "Welcome @a, @b and 4 others"); `ThreadBody(usernames []string, community string) string`; `Announcement(users []bot.Newcomer, community string) map[string]any` (`m.text`, plain and HTML bodies, pills `https://matrix.to/#/@u:s`, `m.mentions.user_ids`); `BatchTxnID(accounts []string) string`.

- [ ] Tests: title shapes above; `TestAnnouncementEscapes` (`<b>` in a username is escaped in `formatted_body`); `m.mentions.user_ids` lists every user; `BatchTxnID` independent of order. Implement, run, commit.

### Task 9: The welcome runner

**Files:** Create `internal/welcome/runner.go`, `internal/welcome/email.go`, `internal/welcome/alert.go`; Test `internal/welcome/runner_test.go`, `internal/welcome/email_test.go`.

**Interfaces:** Consumes Tasks 3–8. Produces `type Runner struct { Store *store.Store; Mbin MbinReader; API MbinAPI; Bot func() MatrixBot; Sender mail.Sender; ... }` with interfaces `MbinReader` (Task 4 methods), `MbinAPI` (Task 5 methods), `MatrixBot` (Task 7 methods plus `Prepare`/`Deliver`); methods `MbinBatch(ctx, now)`, `MatrixBatch(ctx, now)`, `DailyEmail(ctx, now)`, `Prune(ctx, now)`, and `Loop(ctx, r, now func() time.Time, every time.Duration, lead func() bool)`; `Split(limit int) (top, random int)`; `Pick(ranked []mbin.Ranked, greeted func(member int64, entry int64) bool, newcomers []store.Welcomed, top, random int, rnd *rand.Rand, skip map[string]bool) []Recipient`.

Behaviour: Mbin batch ensures the magazine (create when missing, restrict posting; a 403 alerts `mbin-magazine` and stops), posts one thread for the newcomers not yet welcomed since the platform's start and within 21 days, records them, then subscribes every unsubscribed welcomed account (403 alerts `mbin-subscribe`). Matrix batch finds the room once per process, posts one announcement with `BatchTxnID`, records; a refusal alerts `matrix-post`. Daily email at 09:00 claims the day's newcomers, ranks, picks with a day-seeded random source, counts today's sends against the cap, claims, renders, sends, finishes. Alerts email every operator and DM those with a linked Matrix account, once per kind per day.

- [ ] Tests with fakes and a test store: `TestSplit` (50→40/10, 1→0/1, 7→5/2); `TestPickBackfills`; `TestMbinBatchOneThreadForAll`; `TestMbinBatchSkipsMembersBeforeStart`; `TestMbinBatchAlertsWhenNotModerator`; `TestMatrixBatchOneMessage`; `TestEmailRestartKeepsCap`; `TestEmailSkipsUnsubscribedAndNewcomers`; `TestAlertOncePerDay`. Implement, run, commit.

### Task 10: Mail and unsubscribe route

**Files:** Create `internal/mail/templates/welcome.html`, `internal/mail/templates/welcome.txt`, `internal/mail/welcome.go`; Modify `internal/web/server.go`; Test `internal/mail/welcome_test.go`, `internal/web/server_test.go`.

**Interfaces:** Produces `mail.RenderWelcome(newcomers []WelcomeLine, n Names, siteURL, unsubscribeURL string) (Message, error)` with `type WelcomeLine struct { Name, URL string }`; subject `Say hello to N new people on <Meta>` (`1 new person`). The `/u/{token}` route answers a welcome token with `UnsubscribeWelcome` and "You're unsubscribed from welcome emails."; a digest token behaves as before.

- [ ] Tests: subject singular and plural, lines and unsubscribe link present, HTML escapes names; web: welcome token unsubscribes from welcome only, digest token from digest only. Implement, run, commit.

### Task 11: Wiring and documentation

**Files:** Modify `cmd/paisans-engagement-service/main.go`, `README.md`, `CHANGELOG.md`.

- [ ] Build the runner in `serve`; lead is `h.Get() != nil` when Matrix is on, otherwise a goroutine that holds the bot lock; log one line for each part that is off. README: environment table, a *Welcomes* section, the allowlist additions. CHANGELOG bullet. Run the full suite with the database, `gofmt -l .`, `go vet ./...`. Commit.
