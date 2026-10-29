# Changelog

All notable changes to this project are documented here, in the
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format.

## [Unreleased]

### Added

- Every five minutes, one thread in `/m/welcome` names the members who just joined Mbin and subscribes them to it; the bot creates the magazine, moderator only, when Mbin has none.
- Every five minutes, one message in the community room names the members whose Matrix account is new; the bot joins the room, or creates it when there is none.
- A daily welcome email asks up to `ENGAGEMENT_WELCOME_DAILY_LIMIT` active members to greet newcomers they have not greeted, with its own unsubscribe link.
- Operators named in `ENGAGEMENT_OPERATOR_EMAILS` get at most one alert a day for each way the welcomes can fail.

### Changed

- The weekly digest leaves out the local `/m/welcome` magazine.

## [0.2.0] - 2026-10-29

### Added

- A Monday Matrix message from a bot account (Community Bot unless `ENGAGEMENT_MATRIX_BOT_DISPLAY_NAME` names it) with the week's first five threads and microblog posts and a link, for every member with a linked Matrix account.
- Members stop the messages by replying stop, with or without quotes, or leaving the room, and start them again by replying start, resume or restore, or by inviting the bot.
- Each sent digest can be read for four weeks at /d/ and the week's date, by the signed-in member it was sent to, behind the community's sign-in gate.
- A `--preview` option for `send --member` that mails the member the digest a brand-new member would get this week, without claiming them for the week.
- A section in the digest offering each member up to three of the week's new threads and microblog posts that nobody has answered yet.
- A section in the digest and the Matrix message with the week's busiest threads from magazines the member does not follow, one per magazine, naming the magazines they follow or asking them to start.
- A `--matrix` option for `send --member` that sends the member's Matrix message for the week, or with `--preview` a preview message whose link opens for a day, instead of the email.

### Changed

- Unsubscribing from the email forgets which threads a member was shown only when their Matrix messages are off too.
- A bad unsubscribe link answers "This link is not valid."
- The new members figure counts members without a verified email address too.
- Federated threads from a magazine followed only by members without a verified address are now among the week's candidates.
- The digest email's HTML has a head with a title.
- Every thread in the digest shows only its title and magazine, with no counts, featured label or microblog label. A microblog post is quoted instead of titled.
- The digest email's subject and heading read "This week on" the community rather than "Your week on".
- The Matrix message starts "This week on" the community, as the email does, with a blank line between its sections.
- The Matrix message's plain text gives each thread's link below its title.
- The email's button reads "View Everything".
- Microblog posts and quoted comments in the digest drop their links and end with an ellipsis where one was. A post that is only a link is named by its site.

### Removed

- `ENGAGEMENT_INSTANCE_TITLE`: the button no longer names Mbin.

## [0.1.1] - 2026-10-27

### Added

- A `send` command that sends this week's digest now, to everyone or with `--member` to one member, skipping whoever the week already reached.

## [0.1.0] - 2026-10-24

### Added

- A weekly digest email for every member of a Paisans deployment, about the Mbin threads and microblog posts they have not joined yet.
