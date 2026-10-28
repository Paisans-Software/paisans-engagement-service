# Changelog

All notable changes to this project are documented here, in the
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format.

## [Unreleased]

### Added

- A `--preview` option for `send --member` that mails the member the digest a brand-new member would get this week, without claiming them for the week.
- A section in the digest offering each member up to three of the week's new threads and microblog posts that nobody has answered yet.

### Changed

- Every thread in the digest shows only its title and its magazine, without comment, vote or people counts, the featured label or the microblog label.
- The digest email's subject and heading read "This week on" the community rather than "Your week on".

## [0.1.1] - 2026-10-27

### Added

- A `send` command that sends this week's digest now, to everyone or with `--member` to one member, skipping whoever the week already reached.

## [0.1.0] - 2026-10-24

### Added

- A weekly digest email for every member of a Paisans deployment, about the Mbin threads and microblog posts they have not joined yet.
