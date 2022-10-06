# Changelog

## [v1.2.0] - 2022-10-06

### Added

- Add email-emulator service that will write emails onto the disk instead of sending them to an email server. This is a simple alternative to perform local tests without the need to setup actual email server (in case message sending is not needed).

### Changed

- Participant message generation will use the payload (participant flags) for the email template, so that these can be utilised in emails.

## [v1.1.1] - 2022-09-01

### Changed

- Replacing log.Print instances with custom logger to use log levels.
- Fixing issue, where participant messages did not inlcude a login token.

## [v1.1.0] - 2022-06-03

### Added

- New message type / message sending logic for researcher notifications. This messages can be generated through study rules to send a notification about specific topics to a specified list email addresses.

### Changed

- Updated dependencies (gRPC, study-service), and made necessary adaptations on the Makefile to be able to generate the new api files.

## [v1.0.0] - 2022-03-08

### Added

- New message type for participant messages.

### Changed

- Using new logger library with improved logging format and configurable log level.
  - For message-scheduler use the environment variable `LOG_LEVEL=<level>`. Valid values are `debug`, `info`, `warning`, `error` . Default (if not speficied) is `info`.

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
