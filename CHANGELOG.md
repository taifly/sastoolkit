# Changelog

All notable changes to SAS toolkit are listed here. Versions follow
[Semantic Versioning](https://semver.org).

## [Unreleased]

### Added

- API docs: `docs/API.md` (the app's HTTP API) and `docs/SAS4_API.md` (the
  SAS4 API, with every request in the collection marked as used or not).
- New app icon.
- GitHub release workflow: a version tag builds macOS, Windows and Linux.

## [1.0.0] - 2026-10-25

First release.

### Added

- Desktop window built with Wails v2, and `--web` mode for the browser.
- Users and managers list, loaded once and filtered by profile, manager,
  status, search text and expiry range.
- Bulk operations for users: add traffic, deposit and withdraw, activate,
  extend, change profile, cancel, create and rename from CSV, delete, enable
  and disable.
- Bulk operations for managers: deposit, withdraw, reward points and pay debt.
- Dry run on by default, a per-row CSV report for every job, and Retry failed.
- Bulk change through SAS4's server-side Bulk Changes, with the match count
  checked again before sending.
- Reports: active, new and expired users per day, week or month, and each
  user's last session, with CSV and XLSX export.
- Native Save dialog for downloads in the desktop window.
