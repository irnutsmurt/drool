# CLAUDE.md

Guidance for Claude Code and other assistants working in this repository.

## UI work follows the DROOL Design Standard

Every change to the web UI follows **[DESIGN-STANDARD.md](DESIGN-STANDARD.md)**, final for 1.0. That covers pages,
components, styles and text under `web/`, and the browser tests in `web/e2e/`.

- **Read it before starting UI work.** Use the building blocks and interaction patterns it names (§4, §5) instead of
  writing new ones, and use its words for host states and other terms (§4, §7).
- **Meet its requirements before calling the work done.** Run its checklist (§9):
  - layout at 1440, 1280, 1024 and 390 px, in both themes;
  - keyboard path and focus;
  - accessible names that start with the visible text;
  - contrast and touch targets;
  - honest loading, empty, stale and unknown states;
  - tests, and the user guide when user-visible behaviour changes.

  Say in the commit message which checks ran, and what wasn't checked.
- **Honest state and safeguards are release blockers** (§8). Never present a desired, simulated, stale or unknown state
  as what is happening now; where the server can't know, the UI says so. The server decides such states, and the UI
  words them.
- **No new exceptions.** Don't relax a rule to make a change pass, and don't add to the standard's known limitations
  (§10) without the maintainer's agreement. New work meets the rule even where §10 lists an older shortfall.
- **Departing from it is a decision, not a drift.** If a change needs a different convention, say so explicitly, and
  update DESIGN-STANDARD.md in the same change with the reason. Don't redesign established patterns, navigation or the
  host workspace without a demonstrated problem.
- **Keep it in proportion.** Don't chase minor cosmetic imperfections. A small usability issue isn't a release blocker
  by itself; the standard's introduction says what is.

## Where things are

- **Product design and its principles:** [DESIGN.md](DESIGN.md). The UI principles are §2.5 and §2.7.
- **What was built, milestone by milestone:** [ARCHITECTURE-ALPHA.md](ARCHITECTURE-ALPHA.md).
- **The user guide:** `docs/wiki/`, published to the GitHub wiki. It is the canonical user documentation.
- **Release process:** [RELEASING.md](RELEASING.md).

## Checks

- **Web:**
  ```sh
  cd web && npm run typecheck && npm test
  ```
  `npm run build` is needed before the Go build and the browser tests (the UI is embedded).
- **Go:** `gofmt`, `go vet ./...` and `go test ./...`. CI also runs `make test-race`.
- **Browser suite:**
  ```sh
  go test -tags integration -run TestBrowser ./test/integration
  ```
  It needs Docker, Chromium and `npm ci` in `web/`. Set `DROOL_E2E_FILES=a,b` to run only some `web/e2e/*.test.mjs`
  files.
