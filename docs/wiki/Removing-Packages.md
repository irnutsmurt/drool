# Removing packages

Removing a package can take other packages with it: everything that depends on it. DROOL therefore
never removes anything you didn't name without asking you first, per host, after showing you exactly
what would go.

In the examples, Alex removes `telnet` from `web-01`, where it was installed by accident.

## Before you start

The same as [Installing Packages](Installing-Packages.md): **jobs.execute**, APT or DNF, sudo or
root, and on Shepherd hosts the **Managed** or **Full** preset.

## Steps

1. **Runs → New run → Packages**, **Action: Remove**.
2. **Packages:** `telnet` (several names separated by spaces are fine).
3. Choose the **Targets** (here `web-01`).
4. Click **Preview changes**. For each host, DROOL shows what a removal would remove. If that includes
   packages you didn't name (dependencies, or packages that depend on the one you remove), a red box
   says *N hosts would remove other packages*, and lists them per host.
5. Read the list. If it's what you expect, tick **Allow these removals**. If not, change the package
   list, or remove fewer hosts.
6. Click **Run on 1 host** and confirm.

## What DROOL does on each host

- It simulates the removal again right before doing it. A host where the simulation shows a removal
  you didn't allow stops there, unchanged (*ValidationError: DROOL refused to make this change*).
- Then it removes the package: `apt-get -y remove telnet` on APT hosts, `dnf -y remove telnet` on DNF
  hosts.
- **On Debian and Ubuntu, configuration files stay** (it's a *remove*, not a *purge*). DROOL has no
  purge action; run `sudo apt-get purge telnet` as a [command](Running-a-Command.md) if you need it.
- **On DNF hosts**, removing a package can also remove dependencies nothing else needs anymore. The
  preview shows them; they count as removals you must allow.
- A package that isn't installed is left alone and reported as **no change**.

## Related

[Installing Packages](Installing-Packages.md) · [Desired State: what it is](What-Desired-State-Is.md)
(keep a package absent, and get told if it comes back)
