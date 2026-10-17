# DROOL Design Standard

The conventions of DROOL's web UI as built and verified for 1.0. They are rules for every change under `web/`. The
reasons behind them are in [DESIGN.md](DESIGN.md) §2.5 and §2.7, and the as-built record is in
[ARCHITECTURE-ALPHA.md](ARCHITECTURE-ALPHA.md) §41.

This standard describes what has been validated; it doesn't freeze every layout. When a convention has to change,
change it here in the same change as the code, and say why. Don't let the code drift away from it quietly.

Apply it in proportion. It guides new work and shouldn't stand in the way of a reasonable improvement, and minor
cosmetic imperfections aren't worth chasing. A small usability issue isn't a release blocker by itself, and neither is
a known limitation that is documented, safe and doesn't get in the way of normal use. Weakening a safeguard or the
honest presentation of state (§8) always is.

---

## 1. The principle: simple by default, powerful by choice

- **The first screen answers the common question.** Detail and advanced controls are one step away (More filters,
  Advanced options, Show all units, Display options) and are never a prerequisite.
- **Defaults are the recommended choice.** For example, a run on several hosts gets the canary rollout. A choice the user
  made is never replaced by a default.
- **Plain words first.** A themed name (Pack, Shepherd) always comes with its conventional term (§7).
- **Production-conscious.** The common case being a homelab never justifies an unsafe shortcut (§8).

## 2. Navigation

### Global

- **The sidebar** lists direct destinations under headings that are only labels (Fleet, Observe, Automate, Protect),
  with Settings set apart. It is defined in one place, `web/src/components/layout/nav.ts`. Every destination has a
  stable address, and deep links work. Below 1024 px the sidebar becomes the menu button's drawer.
- **The navigation search** (Ctrl+K or `/`) finds pages, settings and records: hosts, Packs, services, active alerts and
  recent runs, limited to what the role may see. A new page is registered in `nav.ts`, and in `lib/navsearch.ts` when it
  has a setting or a question people ask ("can A reach B?").
- **Permissions shape the UI.** What a role can't view is absent from navigation and search. What it can view but not
  change is read-only, and the page says so once with `ReadOnlyNote`.
- **Every page has one `h1`** (`PageHeader`, or the host workspace's header). The browser title and the screen-reader
  announcement of a navigation come from it automatically (`components/layout/PageTitle.tsx`). Never set
  `document.title` by hand inside the app shell.

### A host's workspace

- **Everything about one host lives at `/hosts/{id}/…`** under one header (`components/hosts/workspace/HostLayout.tsx`):
  - what the host is: OS, address, how DROOL manages it;
  - its state, each part labelled: Connection, Last check, Health, open alerts, maintenance, a run in progress;
  - Run command, Install updates and Actions ▾.
- **The sections** are Overview, Monitoring, Files, Network, Firewall, Desired State, Management and Activity, each at a
  stable address (`workspace/sections.ts`).
- **A new host-scoped feature** becomes a section or lives inside one. It never adds a second host header or its own
  host summary.
- **Notices that matter anywhere** (an unapproved or changed host key, unreachable, a restore reconciliation) show under
  the header on every section.

### Local views

At most one row of local navigation per page:

- `LocalNav` (links) when each view is its own place;
- `Tabs` / `TabPanel` (the ARIA tabs pattern, which may keep `?tab=` in the address) when the views switch what one page
  shows.

Never nest tab rows. Don't hand-roll `role="tab"`: `Tabs` provides the keyboard behaviour.

## 3. Information hierarchy and progressive disclosure

- **Order a page by urgency:** what needs a person (Needs attention), then the current state, then summaries of each
  area that link to the section that owns it, then history.
- **One place owns each thing.** Summaries link to it. There is never a second editor for the same object; the
  settings directory links to the owning page.
- **Dense views** show the common filters, the rest under More filters, and a default view that answers the common
  question.
- **Explanations:** one line of description under a title. Step-by-step help belongs in the user guide (`GuideLink`), not
  in paragraphs on the page.

## 4. Standard building blocks

Reuse these; they already carry the behaviour this standard requires. The files are in `web/src/components/ui/` unless
another path is shown.

| Need | Use | Rules |
|---|---|---|
| Page title, description, actions | `PageHeader` | At most two visible actions. The primary one is filled and comes first. The rest go in an Actions ▾ menu. |
| A section inside a workspace | `SectionHeader` | Smaller than the page header; the workspace header stays the one big title. |
| A group of content | `Card` | The title can be a link to where the content lives. |
| An action | `Button` (`primary`, `secondary`, `danger`, `ghost`) | The label is a verb that names its object. "…" when it opens a dialog or a step before acting. |
| Navigation styled as an action | `primaryLink` or `secondaryLink` (`Button.tsx`) | Use a link, not a button that navigates. |
| A link in text | `plainLink`, or `text-accent` on cards | Links inside a sentence are underlined (a global rule in `index.css`). |
| A form field | `Field` with `Input` or `Select`; `Checkbox` | Every field has a visible label, or `aria-label` when its context labels it visually. |
| Less frequent actions | `MenuButton` (Actions ▾, the ⋯ row menu) | Danger items go last, separated from the rest. |
| A list of records | `Table` (cards on a phone and wherever it doesn't fit) | A page's own table uses `class="rtable"` with `data-label`, `data-primary` and an empty `data-label` for actions (`lib/tableFit.ts` decides when it becomes cards). Dense data can show a compact list instead (`hooks/useTableFits.ts`). |
| A state | `StatusBadge` or `StatusIcon` (`components/monitoring/Status.tsx`) | An icon and a word, never colour alone. See the table below. |
| Nothing, loading, failed | `EmptyState`, `Loading`, `LoadError` (`States.tsx`) | Never show "none" or "0" while loading or after an error. |
| A message | `Alert` (`danger`, `warn`, `ok`) | Say what happened and what to do next. |
| A look at one record | `Inspector` | See §5. |
| A consequential action | `ConfirmDialog` or `confirmAction()` | See §5. |
| A modal task | `Dialog` | Keep the buttons in sight (the `footer` prop). |
| Where a setting comes from | `Provenance`, `ScopeLegend` | Recommended, then Global, then Pack, then Host, then Device: the most specific wins. |
| Filters | toggle buttons with `aria-pressed`, counts in the label | Put them in the address (`?…`) when a filtered view is worth sharing. |

### Status indicators

Distinct concepts get distinct indicators. Where several appear together, each is labelled.

| Concept | Meaning | Words | Component |
|---|---|---|---|
| **Connection** | Whether DROOL can sign in and manage the host (from connection tests and the Shepherd's session) | Ready, Untested, Needs host key, Host key changed, Access denied, Unreachable, Unsupported, Disabled | `HostStatusBadge` |
| **Last check** | Whether the latest status check reached the host | online, offline, stale, not checked yet | `ReachabilityBadge` |
| **Health** | Monitoring's verdict | Healthy, Healthy · N notices, Degraded · N issues (worst issue is a warning), Unhealthy · N issues (something is critical), Stale, No data | `HealthBadge` |
| **Alerts** | Confirmed problems, each with its own life cycle | Critical, Warning; acknowledged, snoozed, resolved | `SeverityBadge`, `AlertCounts` |
| **Managed via** | The management path | Shepherd, SSH, Shepherd not connected → SSH | `ManagementBadge` |

Rules for status indicators:

- "Ready" means only that DROOL can manage the host. Show it with its "Connection" label. The one exception is a list of
  targets to run on, where readiness is the question being asked.
- Never fold distinct conditions into one status. Never present stale or unknown data as healthy.
- Severity is in the words as well as the icon and the colour. A screen reader hears the words; the icons are
  `aria-hidden`.

## 5. Inspectors, confirmations and pages

There are three interaction patterns. Choose by the task.

- **Inspector**: look at one record from a list and act on it (an alert, a connection).
  - The list stays in view beside it; on a phone it takes the whole screen. Only one is open at a time.
  - A follow-up step (acknowledge, classify) replaces the inspector's content and has a Back button. It is never a dialog
    on top of the inspector.
  - Focus moves into the panel when it opens and into the step when a step opens. Back returns focus to the button that
    opened the step. Closing returns focus to the row.
- **Confirmation** (`ConfirmDialog`, `confirmAction()`): an action with consequences.
  - The title names the action and its target ("Remove media-01 from media-servers?"). The body says plainly what
    follows. Cancel has the initial focus, and both buttons stay in sight.
  - Use it only where it adds information or prevents a mistake, not on every button. Never use the browser's
    `confirm()` or `prompt()`.
  - It is never the only safety check: the server enforces permissions, re-authentication, locks and each operation's own
    rules.
- **Review before execution**, for runs (the run review), restores (the restore plan) and firewall changes (preview and
  apply). It shows:
  - what will change;
  - on which hosts;
  - the warnings and conflicts known so far;
  - that nothing has changed yet ("This is the plan: nothing changes until you choose Restore").
  - The executing button says what it does and to how many ("Start run on 3 hosts", "Restore 4 resources").
  - A run on several hosts defaults to the recommended rollout.
- **Dedicated page or wizard**: multi-step work and editors, such as Add host, the baseline, workflow and schedule
  editors, and the firewall designer.

## 6. Responsive layout and accessibility

These are requirements, not aims. Check them at **1440, 1280, 1024 and 390 px**, in **both themes**.

### Layout

- **No horizontal page scroll.** No column or row action sits behind a sideways scroll at any of these widths:
  - a table is an `rtable`, which becomes cards when it doesn't fit;
  - or it has a compact list it switches to (`useTableFits`).
- **Sideways scrolling is allowed only for:**
  - code and YAML;
  - a row of local navigation on a phone;
  - a table whose columns only make sense side by side (`Table mobile="scroll"`), and never for actions.
- Text never overlaps and buttons are never cut off. Long server text wraps (`main` breaks long words). A truncated name
  keeps the full text in a `title`.
- **Nothing that takes focus sits in a visually hidden box.** Cards hide a table's header, except a header cell with a
  control in it (Select all), which stays above the cards (a rule in `index.css`). Give that control a label with
  `rt-card-only` text, as Monitoring does.

### Touch

- On touch screens (`pointer: coarse`), buttons, fields, selects, tabs, menu items, navigation links, checkbox labels
  and a card's title link are at least **44 px tall**. A global rule in `index.css` provides this; don't override
  `min-height` there.
- Icon-only buttons (✕, ⋯, ↑ ↓) are **44 × 44 px**: add `pointer-coarse:min-w-11`.
- A checkbox is tapped through its label, so wrap it in a `<label>`. In a table, add a "Select" that shows only in a card
  (`rt-card-only`), as the Hosts list does.
- A button styled as a link (`underline`, `plainLink`) keeps the height of the text around it, like a link. When it
  stands on its own beside other controls, as More filters does, give it `pointer-coarse:min-h-11`.
- Links inside sentences are exempt. Everything else is at least 24 × 24 px, or has the spacing WCAG 2.5.8 accepts
  instead: a 24 px circle on it touches no other target.

### Colour and contrast

- Use the tokens in `index.css` only. Never hard-code colours in components.
- Text reaches **4.5:1** (3:1 for large text) against what is actually behind it, in both themes, tints included. The
  browser suite's contrast test checks the main pages; add new pages and states to it.
- Status is never colour alone. Chart series also differ by dash and marker.

### Focus and keyboard

- Focus is visible on every Tab stop: the global `:focus-visible` outline, or the border and ring that fields use. Never
  use `outline-none` without a replacement.
- Everything that can be clicked can be reached with Tab, in reading order. The skip link comes first.
- **Menus:** Enter, Space or ↓ opens the menu on its first item. ↑, ↓, Home and End move. Escape closes it and returns
  focus to the button. Tab closes it and moves on. The menu is named by its button.
- **Tabs:** one Tab stop for the row; ← and → (with Home and End) move and select.
- **Dialogs and inspectors:**
  - focus moves in, Tab stays inside, and the page beneath is inert;
  - Escape goes back from a step, otherwise closes;
  - focus returns to what opened it.
- **Popovers** (the security status, the account menu): Escape closes them and returns focus to their button.
- **When the focused element disappears** (the row a confirmed removal took away), focus goes to the dialog on top or to
  the main content, never back to the top of the document (`watchFocusLoss` in `ui/modal.ts`). Don't work around it.
- **A box that scrolls on its own**, such as a long list inside a dialog, is reachable from the keyboard: give it
  `tabIndex={0}`, `role="region"` and a name.

### Screen readers

- Every control has a name, and it **starts with its visible text** (WCAG 2.5.3): "Manage hosts: media-servers", not
  "Open pack media-servers".
- Prefer visible text plus `sr-only` text to `aria-label`. Never put `aria-label` on an element that has no role
  (`p`, `div`, `span`); use `sr-only` text instead.
- Icon-only buttons have an `aria-label`. Decorative icons are `aria-hidden`.
- Table headers exist even when visually empty (`<span className="sr-only">Actions</span>`). Every table has a caption or
  an `aria-label`.
- Navigating announces the new page title (`PageTitle`). Live updates use polite live regions (`role="status"`).
- Respect `prefers-reduced-motion`.

## 7. Terminology

Use one word for one thing, everywhere: in pages, dialogs, the API docs and the user guide.

| Term | Means | Notes |
|---|---|---|
| Host | A Linux machine DROOL manages | |
| Pack | A group of hosts | Shown with "Groups" in navigation and search |
| Shepherd | The DROOL agent (`drool-agent`) | Shown with "DROOL agent" where it is introduced |
| Run | One execution of a command, script or operation (a job) | "Runs" in navigation |
| Schedule, Workflow | A run at set times; saved steps that are reused | |
| Baseline, Desired State | What hosts should have; DROOL's view of whether they do | Drift: differs. Restore managed state: correct it. |
| Alert | A confirmed problem with a life cycle | Acknowledge, Snooze, Ignore, Maintenance |
| Connection, Last check, Health, Managed via | The host states in §4 | Never use "Health" for reachability, "Ready" for health, or "online" for healthy |
| Policy | The firewall rules DROOL wants | "Can A reach B?" is a simulation of the policy |
| Observed connection | A connection DROOL saw (Network Activity) | Classification: what it is |

How to write UI text:

- Buttons are a verb plus an object ("Install updates", "Restore 4 resources"). Use sentence case and no exclamation
  marks.
- A disabled control says why, in its `title` or a note next to it.
- Errors say what failed and what to do. Show them where the action was asked for.

## 8. Operational safety and honest presentation of state

- **Loading is not empty, empty is not zero, and zero is not healthy.** Stale data says it is stale and since when.
  Unknown says unknown.
- **Hypothetical results say so.**
  - The firewall simulation: "A simulation: DROOL works out the answer from the firewall policy it wants for these
    hosts. It sends nothing to the hosts, tries no connection, and changes no policy."
  - The designer: "simulated on this draft: nothing is applied".
- **Previews say they change nothing.** The button that executes names what it does (§5).
- **Destructive actions** use danger styling, are confirmed, and come last in menus.
- **The UI never weakens a server check.** It can explain a refusal (a busy host, a missing permission, a blocked
  management path), but the server decides.
- **Secrets** are never shown again after they are entered, and never logged. Copy buttons are only for public material
  (fingerprints, public keys, install commands).

## 9. Adding to the UI

Before opening a pull request that touches `web/`:

1. **Reuse first.** Use a §4 building block or a §5 pattern if one fits. Create a new shared component only when a pattern
   repeats. Put it in `components/ui` with a doc comment that states the rule it embodies.
2. **Put it in the right place.** A new page goes in `nav.ts` and navigation search. A host feature is a workspace
   section or part of one. A setting lives on its owning page and gets an entry in the settings directory.
3. **Handle every state honestly:** loading, empty, error, stale, unknown and read-only (§8).
4. **Use the glossary's words (§7).** Accessible names start with the visible text.
5. **Check the layout** at 1440, 1280, 1024 and 390 px in both themes: no sideways scroll, nothing cut off, every action
   in sight.
6. **Check accessibility.** There is a keyboard path through the change, focus is visible and returns where it should,
   nothing that takes focus is out of sight, every control has a name, contrast passes, and touch targets are big
   enough.
7. **Test it.** A new flow gets a browser test in `web/e2e/`, with its keyboard path when a pattern is involved. Logic gets
   unit tests in `web/src/**/*.test.ts`. The browser suite's contrast and navigation tests stay green.
8. **Document it.** Update the user guide (`docs/wiki/`) when user-visible behaviour changes, and this standard when a
   convention changes.

Checks: `cd web && npm run typecheck && npm test`, then the browser suite:

```sh
npm run build
go test -tags integration -run TestBrowser ./test/integration
```

For changes that reach across the UI, repeat the 1.0 audit: axe-core's WCAG 2.2 A and AA rules, plus the layout,
contrast, touch-target, focus and hidden-Tab-stop checks at the four widths. Read the per-control findings, not only
the totals. See ARCHITECTURE-ALPHA.md §41.12.
