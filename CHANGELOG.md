# Changelog

All notable changes to SAS toolkit are listed here. Versions follow
[Semantic Versioning](https://semver.org).

## [Unreleased]

## [1.1.0] - 2026-10-28

### Added

- One filter panel shared by Select users, Bulk change, Users over time and
  Last session, with the same filters on every page.
- Last-session filters: session state, MAC, NAS IP, stop reason, and
  upload / download / total ranges in MB or GB. The session scan can be
  started from the filter.
- "Shared with another user" filter: session MAC, session IP (online users)
  and static IP.
- Bulk change takes the panel as extra filters. When any is set, it shows the
  matched users with their sessions and sends the action one user at a time,
  with dry run, a report and Retry failed.
- `/api/sessions/status` returns the stop reasons found in the last scan.

### Changed

- Users over time and Last session use the shared panel in place of their
  own profile, manager, state and search fields. The old request fields
  (`profiles`, `managers`, `states`) still work.

### Fixed

- Optional parts of the import result and the Bulk change preview showed
  the text "null".

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
- API docs: `docs/API.md` (the app's HTTP API) and `docs/SAS4_API.md` (the
  SAS4 API, with every request in the collection marked as used or not).
- App icon.
- GitHub release workflow: a version tag builds macOS, Windows and Linux.
