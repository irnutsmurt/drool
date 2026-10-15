# Classifying traffic

Every observed connection on **Network Activity** says **what it is**: DROOL's own management
traffic, something you classified, a known service, or unknown. Classifying the traffic you recognize
(*NFS media storage*, *Backups to the NAS*) leaves the unknown connections, the ones worth a look,
easy to find.

Classifications are labels. They never block or allow anything, and DROOL never makes firewall rules
from them; what the firewall policy says is a separate column ([Policy Classification](Policy-Classification.md)).

## What each connection is

| What it is | Means | How DROOL knows |
|---|---|---|
| ⛨ **DROOL management** | DROOL's own traffic | DROOL's own evidence (below) |
| ✓ **Expected** | A classification someone made, marked *expected* | a classification, or a label for that one connection |
| ◆ **Classified** | A classification someone made | a classification, or a label for that one connection |
| ≡ **Known service** | Named after its port (*https*, *ssh*), nothing more | its port |
| ? **Unknown** | Nothing says what it is yet | nothing yet |

The connection's details say which, and why: *Matches the classification “NFS media storage”
(pack media-servers → nas-01, TCP 2049). Marked as expected.*

## How DROOL recognizes its own traffic

Only from evidence it has, never from a familiar port. An SSH connection to port 22, or a connection
to 9443, is DROOL's only when DROOL knows it is:

- **DROOL's SSH to a host**: inbound to the host's sshd, from the address the host sees DROOL's SSH
  connections come from (DROOL measures that over SSH);
- **a Shepherd connecting to DROOL**: outbound from the host to the controller endpoint its Shepherd
  reported, by the Shepherd's own process (or an unknown process);
- **a jump host forwarding DROOL's SSH**: outbound from a jump host to the sshd of a host DROOL reaches
  through it;
- **the other end of these**, on the host DROOL itself runs on, when DROOL manages that host too.

Anything else on those ports stays what it is: someone's laptop connecting to SSH is a *Known
service*, not DROOL. When the evidence is missing (no SSH sign-in measured yet, for example), DROOL
leaves it unknown rather than guess. DROOL's own traffic can't be classified as something else.

The **DROOL's own traffic** quick filter, or **What it is → DROOL management**, shows exactly these.

## Classify a connection

1. On **Network Activity**, click a connection.
2. In **What it is**, click **Classify…**.
3. The form starts as **exactly this connection**: this host, this direction, this protocol and port,
   and this remote address. Saved like that, it labels this connection only (*labeled from one
   connection*).
4. To classify **similar traffic** too, widen it:
   - **Hosts**: this host, a Pack's hosts, or any host;
   - **Direction**: outbound, inbound, or either;
   - **Protocol** and **Service port**: or either protocol and any port;
   - **Remote side**: an address or range (CIDR), a DROOL host, a Pack's hosts, or anywhere.

   A classification needs at least a port or a remote side: a label for all of a host's traffic would
   say nothing.
5. **Label**: what it is, for example *NFS media storage*. Tick **This traffic is expected** if it is,
   add a note if you like, and click **Save classification**.

   ![Classify traffic, opened from media-02's connection to 198.51.100.77: the label "Media sync (testing)", hosts media-02, outbound, TCP, service port 8443, remote side the address 198.51.100.77, "This traffic is expected" unticked, a note field, and the explanation that, as it stands, this labels this connection only](images/network-classify.png)

A classification applies at once to the traffic already observed and to everything observed later.
When several match a connection, the most specific wins: a label for one connection first, then the
narrower match (one host before a Pack before any host, one address before a range, a port before any
port).

## The Classifications tab

**Network Activity → Classifications** lists them all, each with what it matches in words
(*pack media-servers → nas-01, TCP 2049*), whether it's expected, whether it was made from one
connection, and when it last changed. **Edit** or **Delete** them there; **Add classification…**
makes one without starting from a connection. Deleting one puts the traffic it matched back to what
it was before: another classification, its port's name, or unknown.

## Who can do what

- Seeing classifications needs **network.read** (administrators, operators and auditors).
- Making, changing and deleting them needs **network.classify** (administrators and operators). It
  doesn't ask for your password again: classifications change nothing on any host.
- Every change is recorded in the [audit log](Audit-Log.md) (`network.classification.create`,
  `.update`, `.delete`), with what it matched.
- A Pack or host named in a classification: removing the host removes its classifications; a Pack
  can't be deleted while a classification uses it ([Packs](Packs.md#delete-a-pack)).

## Related

[What DROOL Can See](What-DROOL-Can-See.md) · [Connection Details](Connection-Details.md) ·
[Policy Classification](Policy-Classification.md)
