# Validation before deployment

A typo in `sshd_config` or `sudoers` can lock you out of a machine. A file deployment can therefore
**check the new file on each host before it replaces the old one**: you give a validation command,
and if it fails on a host, the live file on that host is never touched.

## Before you start

- You're setting up a [file deployment](Deploying-a-File-to-Multiple-Hosts.md).
- The checking program must exist on the hosts (for example `nginx`, `sshd`, `visudo`, `xmllint`).
- On Shepherd hosts without the shell capability (the *Managed* preset with files allowed), only
  validation commands the host's owner listed in `/etc/drool/agent.yaml`
  (`allowed_validation_commands`) are allowed.

## How to write one

In the deployment form, fill in **Validation command (optional)**. Write `{path}` where the command
needs the file's path. DROOL replaces it with the path of the *new* file, which waits in a temporary
file next to the destination.

**Don't put quotes around `{path}`.** DROOL inserts the path already quoted for the shell, so
`"{path}"` or `'{path}'` would add a second, literal pair of quotes and break the command. Write it bare,
as in the examples.

| File | Validation command |
|---|---|
| An sshd configuration | `sshd -t -f {path}` |
| A sudoers file (`/etc/sudoers.d/…`) | `visudo -cf {path}` |
| An nginx configuration | `nginx -t -c {path}` |
| A JSON file | `python3 -m json.tool {path}` |
| An XML file (needs `libxml2-utils` / `libxml2`) | `xmllint --noout {path}` |
| Anything that must contain a certain line | `grep -q '<NetworkConfiguration>' {path}` |

Quote other arguments as you would in a terminal: in the last example the single quotes keep the
shell from treating `<` and `>` as redirections.

The command runs with the same privileges as the deployment: as root when **Use administrator
privileges** is ticked. It succeeds when it exits with 0.

## What happens

On each host, after the new file has arrived:

- **Validation passes** → the file is put in place (and the service reloaded or restarted, if you
  asked).
- **Validation fails** → that host stops: the live file stays exactly as it was, and the host is
  reported as failed with the command's output, so you can see what the checker complained about.
  Other hosts carry on (or pause, with a [rollout](Batches-and-Failure-Thresholds.md)).

The deployment's page shows the validation result for each host: *passed* or *failed*, with the
command.

## A limitation to know

The check sees the new file **on its own**. A program whose configuration is spread over several files
checks the new file in the context of the *old* versions of the others. For example, `nginx -t -c
{path}` treats the new file as nginx's main configuration. That's usually what you want, but not always.

## Related

[Deploying a File to Multiple Hosts](Deploying-a-File-to-Multiple-Hosts.md) ·
[Reloading or Restarting a Service](Reloading-or-Restarting-a-Service.md)
