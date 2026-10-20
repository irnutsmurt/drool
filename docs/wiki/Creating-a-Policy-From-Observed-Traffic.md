# Creating a policy from observed traffic

Writing firewall rules is easier when you start from what really happens on your network. DROOL can
turn an observed connection into a **draft rule** for you. It never saves or applies anything by
itself: you review the rule, save the policy, and apply it.

## Before you start

- You need **firewall.write** (administrators) to change the policy: *Only administrators can turn an
  observed connection into a policy.*
- Only make rules for connections you **understand and want**. An unexpected connection is a
  question to investigate first ([Connection Details](Connection-Details.md#investigating-something-unexpected)).

## From one connection

You can start from a host's **Firewall** section (its **Observed connections** tab lists what DROOL saw
on it, with what the saved policy says about each connection) or from **Network Activity**, which has
every host and more filters. It works in every mode, Monitor included: drafting a rule enforces
nothing.

![web-01's Observed connections tab: what DROOL saw on the host, with what the saved policy says about each connection (Covered by policy, Not in policy), the views All, Inbound, Outbound, Internal, External, New external, Unknown traffic and Not in policy, and Take snapshot for this SSH host](images/firewall-host-observed.png)

1. Click the connection, for example `media-02` → `198.51.100.77`, TCP 8443
   (after Alex learned it's a new plugin that syncs watch history). It opens in a panel beside the
   list.
2. Under **Firewall**, DROOL shows its suggestion, for example *Suggested policy: Allow media-02 →
   198.51.100.77 tcp-8443*.
3. Click **Create policy from observed connection…** at the bottom of the panel. The firewall
   designer opens in the **Rules** view, with the rule added to your draft: *Draft rule
   media-02-198-51-100-77-tcp-8443 was added from an observed connection. It is only part of this
   draft: review it below, check the preview and management paths, then save the policy and apply it
   — or discard it.* If the connection used a port no service describes, DROOL adds a service for it
   too (here `tcp-8443`).
4. Review the rule: click **Edit** to rename it (`media-02-watch-sync`), widen or narrow it (a Pack
   instead of one host, a network instead of one address), or turn the custom service into a named
   one.
5. Check the **Preview**, then click **Save policy**, or **Discard changes**. Saving changes no host
   and no mode: *Saved. Nothing has changed on any host yet.*, with the hosts it affects.
6. Click **Review and apply to media-02…** when you're ready: the review says what changes, and
   whether it switches the host to Enforce ([Applying the Firewall Policy](Applying-the-Firewall-Policy.md)).

![The firewall designer's Rules view after "Create policy from observed connection": a notice says draft rule media-02-198-51-100-77-tcp-8443 was added from an observed connection; the new rule 7 allows media-02 to reach 198.51.100.77/32 on tcp-8443 (TCP 8443) and is marked "new · from an observed connection"; a new service tcp-8443 is listed](images/network-create-policy.png)

## From Guided mode suggestions

A host in **Guided** mode (*Observe, and suggest policies from what is observed for you to approve*)
lists **Suggested policies** on its firewall page, under **Observed connections**: rules that would
cover what DROOL has seen, each with how many connections it covers.

1. Click **Review…** next to a suggestion.
2. If it needs a new service, DROOL asks first: *This suggestion needs a new service …* → **Create the
   service**.
3. Review the rule in the editor, change it if needed, and save it.
4. Apply the policy when you're ready.

## Good to know

- Suggestions are as narrow as the observation: one host and one address. Rules that name Packs and
  networks are usually easier to keep up to date.
- Nothing you do here changes a host until you apply the policy.

## Related

[Policy Classification](Policy-Classification.md) · [Firewall Policy](Firewall-Policy.md) ·
[Applying the Firewall Policy](Applying-the-Firewall-Policy.md)
