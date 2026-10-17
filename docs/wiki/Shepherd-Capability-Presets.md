# Shepherd capability presets

A Shepherd only does what its host allows. That limit, the **ceiling**, is written in a file on the
host, `/etc/drool/agent.yaml`, which only root on that host can change. DROOL can ask for less, but
never for more. Even a DROOL controller in the wrong hands can't make a Shepherd exceed it.

When you install a Shepherd, you choose one of three **presets** for that file.

## The presets

| | **Monitor** | **Managed** | **Full** |
|---|---|---|---|
| In one line | Reports inventory and health. Changes nothing. | Services, reboots and packages from configured repositories. No arbitrary files, no shell. | Everything, including shell and unrestricted files. |
| Monitoring | yes | yes | yes |
| Services (start, stop, restart, enable…) | no | all services | all services |
| Reboot | no | yes | yes |
| Packages and updates | no | only from repositories already configured on the host | any (**root-equivalent**) |
| Files (file manager, file deployment) | no | no | the whole filesystem (**root-equivalent**) |
| Shell (commands and scripts) | no | no | yes, as root (**root-equivalent**) |
| Firewall (DROOL's firewall policy) | no | yes | yes |
| Users | no | no | yes (reserved: no DROOL feature uses it yet) |

**Root-equivalent** marks capabilities that let the controller do anything root can on that host:
running commands as root, rewriting any file (`/etc/sudoers`, systemd units), installing packages from
anywhere (package scripts run as root), managing users. The **Full** preset has all of them. On the
host, `drool-agent presets` prints the same table.

## Which one to choose

- **Monitor** — you only want the Shepherd's continuous monitoring and network visibility, and do any
  changes yourself (or over SSH).
- **Managed** — DROOL should keep the host updated, restart services, reboot it and enforce the
  firewall, but you don't want it to be able to run arbitrary commands or write arbitrary files.
  A good default, and what DROOL suggests.
- **Full** — you want everything DROOL offers through the Shepherd: commands, scripts, the file
  manager, file deployments. Choose it for hosts you'd also give DROOL root SSH access to.

In the examples, `media-01` uses **Full**, so commands, scripts and file deployments work through its
Shepherd.

## What happens when something isn't allowed

If a run asks a Shepherd to do something its ceiling forbids, that host fails with a
**CapabilityError** that names the missing capability. DROOL does **not** retry it over SSH, even
if SSH is configured: a refusal is final. Change the ceiling (below) if the host should allow it.

## Narrowing from DROOL

An administrator can restrict a Shepherd further without touching the host: in the host's
**Management** section, on the **Shepherd (DROOL agent)** card, open **Shepherd details**, then
**Narrow…** next to *What DROOL may do on this host*. In the **Narrow what DROOL may do** dialog you
can switch off capabilities, limit services to a list (**Only these services**, for example
`nginx jellyfin`) and files to paths (**Only these paths**). Only capabilities the ceiling allows can
be ticked. Click **Apply**; **Remove narrowing** returns to the ceiling.

## Changing the ceiling on the host

To widen (or narrow) what a host allows, edit its ceiling as root and restart the Shepherd. For
example, `media-02`'s Managed ceiling looks like this:

```yaml
capabilities:
    monitoring: true
    shell: false
    packages:
        enabled: true
        configured_repositories_only: true
    services:
        enabled: true
    reboot: true
    files:
        enabled: false
    firewall: true
    users: false
```

To allow file deployments only below `/etc/jellyfin`, you could change the `files` part to:

```yaml
    files:
        enabled: true
        allowed_paths: [/etc/jellyfin]
```

Then, on the host:

```sh
sudo systemctl restart drool-agent
```

The indentation matters (YAML). DROOL sees the new ceiling when the Shepherd reconnects.

## Keeping the ceiling absolute

SSH, when it's configured as the fallback, is limited only by the SSH account's own rights. If the
ceiling must be the only limit, turn the fallback off (**Turn off SSH fallback** on the Management
card) or remove DROOL's SSH access to the host. See [SSH Fallback](SSH-Fallback.md).

## Related

[Installing a Shepherd](Installing-a-Shepherd.md) · [SSH vs Shepherds](SSH-vs-Shepherds.md) ·
[SSH Fallback](SSH-Fallback.md)
