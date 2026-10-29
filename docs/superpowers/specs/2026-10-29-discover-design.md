# Discover: busy threads outside what a member follows

## Why

A member who follows one or two magazines can see nothing new in them for a
week and decide the community is dead. Usually it isn't. The activity is in
magazines they don't follow, and a member who doesn't know how Mbin works may
not realise those magazines exist. Discover is the weekly digest's proof of
life: it shows busy threads from magazines the member doesn't follow, with each
magazine's name visible.

**Success:** members open threads outside their bubble and stop reading quiet
as dead.

## Goals

- Show the week's busiest threads from magazines the member doesn't follow,
  spread across magazines.
- Make the magazine name visible on every Discover thread, so the member sees
  that the site is bigger than their feed.
- Put the section in all three places the digest goes: the email (HTML and
  text), the web digest, and the Matrix notice.
- For a member who follows nothing, use the section to nudge them to start
  following.

## Non-goals

- No follow button, subscribe link or call to action beyond the wording. The
  member opens the thread, and following the magazine is up to them.
- No change to how the main list (`Featured` and `More`) is built or ranked.
- No change to the unanswered section beyond leaving out Discover's threads.

## Anti-goals

- A Discover section that repeats a thread already in the digest.
- A lone single-item section. It looks like the opposite of proof of life.
- Federated threads in Discover. The rule that federated content reaches a
  member only through a magazine they follow stays absolute.

## What the member sees

All wording below is a **placeholder**. See *Wording review* at the end.

### Email and web digest

A tinted box after the main list and before the unanswered section. It holds:

1. A heading. Placeholder: "Around Paisans this week", with the instance's
   display name in place of "Paisans" (from `mail.Names`).
2. An explainer line that names the magazines the member follows. Placeholder:
   "You follow Baking and Gardening. Other magazines are busy too."
   If there are many, it names the first few and then "and N more". How many
   counts as "a few" is the builder's call, with 3 as a starting point.
3. Up to 3 threads. Each shows its title link and its magazine name, styled
   like the rest of the digest (`toView`, a quoted body for a microblog post).
   No engagement figures.

The tint is a light green in the family of the magazine-name green (`#22aa66`),
e.g. `#f3faf6`, with the same radius and padding as the featured box. It must
render in the same email clients the rest of the template targets: inline
styles and table-safe markup only.

The text email gets the same section: heading, explainer and items, using the
existing `item` template.

The web digest renders the email's HTML (`mail.RenderPage`), so it gets the
section without extra work.

### Matrix notice

The main list keeps its cap of 5 lines (`maxTitles`). Below it, before the
"Read the whole digest" line:

- A heading line. Placeholder: "Busy where you don't follow:"
- Up to 2 lines, each `title · Magazine`, with the title linked in the HTML
  form and a quoted body for a microblog post, as the main list does.

This is on top of the 5. A notice can therefore carry up to 7 thread lines.
Both the plain `body` and the HTML `formatted_body` get it, and the HTML keeps
the `<br>` spacing approach `weeklyHTML` uses now.

### A member who follows nothing

For them every magazine is one they don't follow. Discover still appears, but
its explainer becomes a nudge to start following. Placeholder: "You don't
follow any magazines yet. Here's what's busy." The Matrix heading gets a
matching variant.

A brand-new member is this case, and so is the digest preview
(`Runner.blankDigest`, which builds for `digest.View{}`). Previews therefore
show the nudge wording.

## How threads are picked

The candidates are the same items `Build` gets: `Source.Hot`, the week's top
200 by score and comments. No new query is needed for the threads themselves.

A thread is eligible for Discover when all of these hold:

- Its magazine is not in `v.Subscribed`.
- It is local (`!Federated`).
- It passes every exclusion `Build` applies: not joined, not in a blocked or
  banned magazine, not by a blocked author.
- No earlier digest showed it (`v.Shown`). *Builder's call, made in
  brainstorming:* unlike the main list, Discover does not bring a shown thread
  back for new comments. A repeat weakens "look what else is happening".
- Its magazine is not one the featured thread or any `More` thread is in. The
  main list is ranked with only a small boost for followed magazines, so for a
  member who follows a few quiet ones it is already mostly unfollowed
  magazines. Discover shows *other* magazines, so the member sees more names,
  not the same ones twice. On a small site this leaves the section out more
  often, and that is accepted.

Eligible threads are ranked by the same engagement figures `rank` uses. For a
local thread in an unfollowed magazine the weight is 1.0, so `rank` can be
reused as it stands. Then they are taken in rank order, **at most one per
magazine**, up to 3. Ties break as `Build`'s sort does, so the pick is
deterministic.

If fewer than 2 threads qualify, `Discover` is left empty and no section
appears in any channel. The Matrix notice shows the first 2 of the 3, in
order.

The unanswered section leaves out Discover's threads too. `AddUnanswered`
already skips everything in `taken`, so Discover is filled first and its keys
go into `taken`.

## Data and storage

- `digest.Digest` gains the Discover items and what the explainer needs: the
  names of the member's followed magazines, or the count beyond the few it
  names. The digest is stored as JSON per member and week
  (`Store.SaveDigest`), and the web page and the Matrix notice render from the
  stored copy. So the explainer's inputs must be in the stored struct, not
  looked up again at render time.
- *Builder's call, made in brainstorming:* the explainer names and counts only
  followed magazines the member can open. That means magazines `hotSQL` would
  accept (visible, not deleted or marked for deletion, not adult) that the
  member is not banned from. Remote magazines count. A member whose only
  follows fail that test gets the nudge. Store both `name` and `title` for each
  named magazine, so the wording review can pick which one shows.
- `View.Subscribed` holds magazine IDs only. The followed magazines' display
  names need a source. Where they come from (a new choices row, a join in the
  existing choices query, or a lookup in the Hot items) is the builder's call.
  It must not add a round trip per member if one query can serve.
- A digest stored before this change has no Discover fields. It renders
  without the section. No migration.
- `sent()` must include Discover's threads, so `Shown` covers them and they are
  not repeated in a later week.

## Constraints

- Existing package boundaries hold. `digest` stays pure: it gets the items and
  the view, reads nothing and writes nothing. Picking goes in `digest`,
  rendering in `mail` and `bot`, and wiring in `run`.
- Tests follow the existing style in each package: table and fixture tests in
  `digest`, golden-ish render tests in `mail` and `bot`, and `run` tests for the
  wiring and for `sent()`.
- The README describes what the digest contains. Update it to cover Discover.
  Write it as the current state, with no history and no rejected
  alternatives.
- Add a CHANGELOG bullet under Unreleased, in the repo's existing style.

## Wording review (before the pull request)

The founder wants to proof and tweak every word before the PR opens. Once the
feature works, and before `gh pr create`:

1. Render real output from sample data for each case:
   - email HTML
   - email text
   - Matrix plain body
   - Matrix HTML
   - a member following 2 magazines
   - a member following many (the "and N more" case)
   - a member following nothing (the nudge)

   A test fixture or a small throwaway render harness is fine. If the staging
   Mbin has data, `send --preview` and `send --matrix --preview` against it is
   better.
2. Show them to the founder (the visual companion or an artifact works for
   the HTML) and go through the wording line by line: the heading, the
   explainer for each case, the Matrix heading, and how the magazine name is
   written (the digest writes `!name` today).
3. Apply the edits, update the tests' expected strings, and then open the PR.

Every placeholder string in this document is expected to change in that
review.

## Left to the builder

- Field and function names (`Discover`, `AddDiscover` or similar).
- Where the followed magazines' names come from (see *Data and storage*).
- How many names the explainer lists before "and N more".
- Exact HTML for the tinted box, within the template's existing conventions.
