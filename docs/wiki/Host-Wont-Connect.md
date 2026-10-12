# Host won't connect

The host shows **Unreachable**, or the connection test fails at **Reachable**: DROOL can't open a
network connection to the host's SSH port. Nothing about keys or passwords has been tried yet.

## What the message says

| Message | Means | Check first |
|---|---|---|
| *connection to web-01 refused* | The machine answered, but nothing listens on that port. *Is sshd running and listening on this port?* | sshd is running (`systemctl status ssh` or `sshd`), and the port in DROOL is right |
| *connection to web-01 timed out* | No answer at all. *Check that the host is up, the address and port are right, and no firewall drops SSH from the controller.* | The host is on, the address is right, no firewall in between |
| *no network route to web-01* | *The host is down or not reachable from the controller's network* | The address is in a network the controller can reach (VLANs, VPNs) |
| A name that doesn't resolve | *Check the host name, or use an IP address.* | The name resolves **on the controller** (not just on your laptop) |
| *the jump host refused to forward the connection* | *The jump host's sshd may have AllowTcpForwarding disabled.* | `AllowTcpForwarding yes` on the jump host ([Jump Hosts](Jump-Hosts.md)) |

## Steps

1. **Is the host on?** Look at its console, or its power and network lights.
2. **Is the address right?** Open the host's page → **Edit**, and compare the address and SSH port with
   the machine's real ones (`ip -brief address` on the host).
3. **Can the controller reach it?** From the machine DROOL runs on:

   ```sh
   nc -vz 10.10.40.21 22
   ```

   (or `ssh -p 22 drooladmin@10.10.40.21`). In Docker, the controller connects from the container: a
   host the Docker machine can reach, the container usually can too, unless a firewall on the Docker
   machine blocks forwarded traffic.
4. **Is a firewall in the way?** A firewall on the host (including DROOL's own, if someone removed the
   management path with the [emergency override](Emergency-Override.md)), or on a router between
   the controller and the host.
5. Fix the cause, then **Test connection** on the host's page. DROOL also checks every host every 5
   minutes, and the status changes by itself once the host answers.

## Shepherd hosts

A host with a Shepherd keeps working through the Shepherd even when SSH doesn't. If the Shepherd is
offline too, see [Shepherd Offline](Shepherd-Offline.md).

## Related

[Testing a Connection](Testing-a-Connection.md) · [Authentication Failed](Authentication-Failed.md) ·
[Jump Hosts](Jump-Hosts.md)
