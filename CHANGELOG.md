# Changelog

All notable changes to this project are documented here, in the
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format.

## [Unreleased]

### Added

- A Monday Matrix message from a bot account (Community Bot unless `ENGAGEMENT_MATRIX_BOT_DISPLAY_NAME` names it) with the week's first five threads and microblog posts and a link, for every member with a linked Matrix account.
- Members stop the messages by replying stop, with or without quotes, or leaving the room, and start them again by replying start, resume or restore, or by inviting the bot.
- Each sent digest can be read for four weeks at a signed /d/ link.
- A `--preview` option for `send --member` that mails the member the digest a brand-new member would get this week, without claiming them for the week.
- A section in the digest offering each member up to three of the week's new threads and microblog posts that nobody has answered yet.
- A `--matrix` option for `send --member` that sends the member's Matrix message for the week, or with `--preview` a preview message whose link opens for a day, instead of the email.

### Changed

- Unsubscribing from the email forgets which threads a member was shown only when their Matrix messages are off too.
- A bad unsubscribe link answers "This link is not valid."
- The new members figure counts members without a verified email address too.
- Federated threads from a magazine followed only by members without a verified address are now among the week's candidates.
- The digest email's HTML has a head with a title.
- Every thread in the digest shows only its title and magazine, with no counts, featured label or microblog label. A microblog post is quoted instead of titled.
- The digest email's subject and heading read "This week on" the community rather than "Your week on".

## [0.1.1] - 2026-10-27

### Added

- A `send` command that sends this week's digest now, to everyone or with `--member` to one member, skipping whoever the week already reached.

## [0.1.0] - 2026-10-24

### Added

- A weekly digest email for every member of a Paisans deployment, about the Mbin threads and microblog posts they have not joined yet.
