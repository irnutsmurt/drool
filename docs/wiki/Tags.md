# Tags

A tag is a short, free-form label on a host, such as `web`, `ubuntu`, `media` or `role:web`. Tags
help you **find** hosts and **pick** them for a run. Unlike [Packs](Packs.md), they carry no settings.

In the examples, `web-01` has the tags `web` and `ubuntu`, and the media servers have `media` and
`debian`.

## Before you start

Changing a host's tags needs **hosts.write** (administrators). Everyone who can see hosts sees
their tags.

## Add or change tags

- **When adding a host**, type them in **Tags**, separated by commas: `web, ubuntu`.
- **Later**, open the host, choose **Actions → Edit host details**, change **Tags** and click
  **Save**.

A tag is either a single word (`web`) or a `key:value` pair (`env:prod`, `role:web`):

- the word or key: letters, digits, `.`, `_` and `-`, up to 64 characters;
- the value after the colon: the same, plus `:` and `/`, up to 128 characters;
- no spaces; at most 32 tags per host.

DROOL treats each tag as plain text: `role:web` is one tag, matched as a whole.

## Where tags are used

| Where | How |
|---|---|
| **Hosts** list | The **TAGS** column, and the search box (*Search name, address, tag…*) |
| **Search** (**Ctrl+K**) | Finds hosts by their tags too ([First Login](First-Login.md#3-find-your-way-around)) |
| **New run** | **Tags** under the targets: *Hosts with any of these tags*. Type one or more, e.g. `media debian`; a host needs only one of them to be included |
| **Schedules** and file deployments | The same target picker as runs |

Tags in a schedule's targets are looked up again at every run, so tagging a new host `media` today
includes it in tomorrow's scheduled run.

## Tags or Packs?

- Use a **tag** to describe a host (its role, OS, location) and to pick hosts ad hoc.
- Use a **Pack** when hosts should be *managed* together: shared monitoring adjustments, Desired
  State, firewall rules, notification routing, maintenance. Firewall rules and Desired State work with
  Packs and hosts, not with tags.

## Related

[Packs](Packs.md) · [Adding a Host](Adding-a-Host.md) · [Running a Command](Running-a-Command.md)
