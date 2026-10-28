# Changelog

All notable changes to this project are documented here, in the
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format.

## [Unreleased]

### Added

- A Monday Matrix message from a Digest Bot account, with the week's first three threads and a link, for every member with a linked Matrix account on the deployment's homeserver.
- Members stop the messages by replying stop or leaving the room, and start them again by replying start, resume or restore, or by inviting the bot.
- Each sent digest can be read for four weeks at a signed /d/ link.

### Changed

- Unsubscribing from the email forgets which threads a member was shown only when their Matrix messages are off too.
- A bad unsubscribe link answers "This link is not valid."
- The new members figure counts members without a verified email address too.
- Federated threads from a magazine followed only by members without a verified address are now among the week's candidates.
- The digest email's HTML has a head with a title.

## [0.1.1] - 2026-10-27

### Added

- A `send` command that sends this week's digest now, to everyone or with `--member` to one member, skipping whoever the week already reached.

## [0.1.0] - 2026-10-24

### Added

- A weekly digest email for every member of a Paisans deployment, about the Mbin threads and microblog posts they have not joined yet.
