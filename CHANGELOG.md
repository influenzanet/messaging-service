# Changelog

## [v0.9.3] - 2021-07-28

### Changed

- For newsletter message type, the weekday setting of the user can be ignored.
- API arguments for send messages to all users and study participants extended to use "IgnoreWeekday" (boolean), to control if for newsletter type, the filter should ignore reminder weekday of the user.

## [v0.9.2]

### Added

- Email templates can use the language attribute that would contain the preferred language from the user model. Example usage added to the [docs](docs/email-templates.md).

### Changed

- "Auto email" definitions can contain a label, so that admins can describe the intent for the specific config.
- Updated dependencies (reflected in go.mod).
- Email-templates documentation includes new possibilities related to the above changes of this release.