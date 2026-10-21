import { useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router";
import { useHosts } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Dialog } from "@/components/ui/Dialog";
import { cx } from "@/components/ui/cx";
import { errorMessage } from "@/lib/format";
import { ApplyDialog } from "../ApplyDialog";
import { useQueryClient } from "@tanstack/react-query";
import { fwHostsQuery, useFwServices } from "../queries";
import { addRule, deleteRule, uniqueRuleName, upsertService, type MgmtPath, type Rule, type Service, type ServiceInfo, type TopologyMode } from "./model";
import { useNetFlow } from "@/components/network/queries";
import { useDesigner } from "./useDesigner";
import { narrowQuery, useMediaQuery } from "@/hooks/useMediaQuery";
import { Topology, type Selection } from "./Topology";
import { RulesView } from "./RulesView";
import { YamlView } from "./YamlView";
import { RuleDialog } from "./RuleDialog";
import { AfterSave, applicable, Inspector, MgmtAlerts, PreviewPanel } from "./Panels";
import { TabPanel, Tabs } from "@/components/ui/Tabs";

const views = [
  { id: "topology", label: "Topology" },
  { id: "rules", label: "Rules" },
  { id: "yaml", label: "YAML" },
] as const;
type View = (typeof views)[number]["id"];

/** The Firewall Designer: Topology, Rules and YAML over one draft. */
export function DesignerPage() {
  const d = useDesigner();
  const qc = useQueryClient();
  const { can } = useAuth();
  const canWrite = can("firewall.write");
  const hostsQ = useHosts();
  const svcQ = useFwServices();
  const [params, setParams] = useSearchParams();
  // Topology is a spatial, desktop interaction: narrow screens open the
  // Rules view (same policy document) unless a view was chosen.
  const narrow = useMediaQuery(narrowQuery);
  const view = (params.get("view") as View) ?? (narrow ? "rules" : "topology");
  const setView = (v: View) => {
    if (view === "yaml" && v !== "yaml") d.normalizeYAML();
    const next = new URLSearchParams(params);
    next.set("view", v);
    setParams(next, { replace: true });
  };
  const [selection, setSelection] = useState<Selection>(null);
  const [ruleDialog, setRuleDialog] = useState<{ index?: number; from?: string; to?: string } | null>(null);
  const [focus, setFocus] = useState<string[]>();
  const [side, setSide] = useState<"details" | "preview">("preview");
  // On the Topology view the side panel gives way to the canvas while there
  // is nothing to show in it (nothing selected, no unsaved change): the map
  // gets the full width, so the policy stays legible. Selecting something or
  // changing the draft brings it back; the toggle opens or hides it by hand.
  const [panelChoice, setPanelChoice] = useState<boolean | null>(null);
  // The save confirmation (a draft blocking a management path); review:
  // the save was "Save and review application…".
  const [confirmSave, setConfirmSave] = useState<{ review: boolean } | null>(null);
  const [applyTo, setApplyTo] = useState<string[] | null>(null);
  const [afterSaveDismissed, setAfterSaveDismissed] = useState(false);
  const topoMode = (params.get("topo") as TopologyMode | null) ?? "configured";
  const setTopoMode = (m: TopologyMode) => {
    const next = new URLSearchParams(params);
    if (m === "configured") next.delete("topo"); else next.set("topo", m);
    setParams(next, { replace: true });
  };

  // "Create policy from observed connection": the suggested rule becomes
  // part of the draft — never saved or applied here. Validation, the
  // management-path checks, preview, save and apply follow as for any edit.
  const suggestId = params.get("suggest");
  const suggestion = useNetFlow(suggestId ? Number(suggestId) : undefined);
  const [suggested, setSuggested] = useState<string>();
  useEffect(() => {
    const sg = suggestion.data?.suggestion;
    if (!suggestId || d.loading || !suggestion.data) return;
    const next = new URLSearchParams(params);
    next.delete("suggest");
    if (sg && canWrite) {
      let doc = d.draft;
      if (sg.new_service && !doc.services.some((x) => x.name === sg.new_service!.name) && !services.some((x) => x.builtin && x.name === sg.new_service!.name)) {
        doc = upsertService(doc, { name: sg.new_service.name, description: sg.new_service.description, entries: sg.new_service.entries as Service["entries"] });
      }
      const name = uniqueRuleName(doc, sg.rule.name);
      d.edit(addRule(doc, { ...(sg.rule as Rule), name }));
      setSuggested(name);
      next.set("view", "rules");
    }
    setParams(next, { replace: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [suggestId, d.loading, suggestion.data]);

  const hosts = useMemo(() => (hostsQ.data?.items ?? []).map((h) => ({ id: h.id, name: h.name })), [hostsQ.data]);
  const services = useMemo(() => (svcQ.data?.items ?? []).map((s) => ({ name: s.name, description: s.description, builtin: s.builtin, entries: s.entries })) as ServiceInfo[], [svcQ.data]);

  // Leaving with unsaved changes asks first.
  useEffect(() => {
    if (!d.dirty) return;
    const h = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", h);
    return () => window.removeEventListener("beforeunload", h);
  }, [d.dirty]);

  // What to highlight: blocked management segments and the rules causing them.
  const highlight = useMemo(() => {
    const policies = new Set<string>();
    const nodes = new Set<string>();
    const byId = new Map(hosts.map((h) => [h.id, h.name]));
    for (const p of d.management) {
      for (const h of p.hops) {
        if (h.status !== "blocked") continue;
        h.blocked_by.forEach((b) => policies.add(b));
        for (const id of [h.from, h.to]) nodes.add(id === "controller" ? "controller" : `host:${byId.get(id) ?? ""}`);
      }
    }
    return { policies: [...policies], nodes: [...nodes] };
  }, [d.management, hosts]);

  const showPath = (p: MgmtPath) => {
    const byId = new Map(hosts.map((h) => [h.id, h.name]));
    setView("topology");
    setFocus(p.hops.flatMap((h) => [h.from === "controller" ? "controller" : `host:${byId.get(h.from)}`, `host:${byId.get(h.to)}`]));
  };

  if (d.loading) return <p className="text-sm text-muted">Loading the firewall policy…</p>;
  if (d.loadError) return <Alert>{errorMessage(d.loadError)}</Alert>;

  const blocked = d.management.filter((p) => p.blocked).length;
  // Saving changes no host. "Save and review application…" then opens the
  // review for the hosts the save affected that DROOL can apply to now (the
  // review asks how each host not in Enforce mode should handle it); when
  // there is none, the after-save summary says why.
  const doSave = (review: boolean) => {
    setAfterSaveDismissed(false);
    d.save.mutate(undefined, {
      onSuccess: async (res) => {
        if (!review || !canWrite || res.hosts.length === 0) return;
        // Each host's status now, as the after-save summary shows it.
        const live = await qc.fetchQuery(fwHostsQuery).catch(() => null);
        const ids = res.hosts.map((h) => live?.items.find((x) => x.host_id === h.host_id) ?? h).filter(applicable).map((h) => h.host_id);
        if (ids.length > 0) setApplyTo(ids);
      },
    });
  };
  const save = (review: boolean) => (blocked > 0 ? setConfirmSave({ review }) : doSave(review));
  // Offered when the draft changes some host's firewall.
  const affectsHosts = (d.check?.impact.length ?? 0) > 0;
  const saveDisabled = !d.dirty || d.yamlBroken || !d.check?.valid || d.save.isPending || d.checking;
  const showPanel = view !== "topology" || (panelChoice ?? (selection != null || d.dirty));

  return (
    <div className="flex flex-col gap-3" data-testid="designer">
      <div className="flex flex-wrap items-center gap-2">
        {/* Three views of one policy document: a view switcher, not more navigation. */}
        <div role="group" aria-label="View the policy as" className="inline-flex overflow-hidden rounded-ui border border-line">
          {views.map((v) => (
            <button key={v.id} type="button" aria-pressed={view === v.id} onClick={() => setView(v.id)}
              className={cx("px-3 py-1.5 text-sm", view === v.id ? "bg-accent text-accent-fg" : "bg-surface hover:bg-surface-2")}>
              {v.label}
            </button>
          ))}
        </div>
        <span className="text-xs" aria-live="polite" data-testid="designer-status">
          {d.checking ? <span className="text-muted">Checking…</span>
            : d.yamlBroken ? <Badge tone="danger">YAML has errors</Badge>
              : d.check && !d.check.valid ? <Badge tone="danger">{d.check.problems.length} problem(s)</Badge>
                : d.dirty ? <Badge tone="warn">Unsaved changes</Badge> : <Badge tone="ok">Saved</Badge>}
          {blocked > 0 && <> <Badge tone="danger">management path blocked</Badge></>}
        </span>
        <div className="ml-auto flex gap-2">
          {view === "topology" && (
            <Button size="sm" variant="ghost" aria-expanded={showPanel} aria-controls="designer-side"
              onClick={() => { if (showPanel) setSelection(null); setPanelChoice(!showPanel); }}>
              {showPanel ? "Hide side panel" : "Details and preview"}
            </Button>
          )}
          {canWrite && <Button size="sm" variant="ghost" disabled={!d.canUndo} onClick={d.undo}>Undo</Button>}
          {canWrite && <Button size="sm" variant="ghost" disabled={!d.dirty} onClick={d.discard}>Discard changes</Button>}
          {canWrite && (
            <Button size="sm" variant={affectsHosts ? "secondary" : "primary"} disabled={saveDisabled} onClick={() => save(false)}>
              Save policy
            </Button>
          )}
          {canWrite && affectsHosts && (
            <Button size="sm" variant="primary" disabled={saveDisabled} onClick={() => save(true)}>
              Save and review application…
            </Button>
          )}
        </div>
      </div>
      {!canWrite && <Alert tone="warn">You can explore and simulate; only administrators can change the firewall policy.</Alert>}
      {d.staleBase && <Alert tone="warn">Someone else saved the firewall policy since you opened it. Saving will be refused: copy your changes from the YAML view, then discard and reapply them.</Alert>}
      {d.save.error && <Alert>{errorMessage(d.save.error)}</Alert>}
      {d.save.isSuccess && !d.dirty && d.save.data && !afterSaveDismissed && (
        <AfterSave hosts={d.save.data.hosts} noHost={d.save.data.no_host_rules ?? []} canApply={canWrite} onReview={setApplyTo}
          onDismiss={() => setAfterSaveDismissed(true)} />
      )}
      {suggested && !(d.save.isSuccess && !d.dirty) && (
        <Alert tone="warn">
          Draft rule <strong>{suggested}</strong> was added from an observed connection. It is only part of this draft: review it below, check the preview and
          management paths, then save the policy — or discard it. Saving changes nothing on hosts: DROOL then lists the hosts it affects, to review and apply.
        </Alert>
      )}
      {suggestId && !canWrite && <Alert tone="warn">Only administrators can turn an observed connection into a policy.</Alert>}
      <MgmtAlerts management={d.management} onShow={showPath} />

      <div className={cx("grid min-h-[38rem] gap-3", showPanel && "xl:grid-cols-[1fr_24rem]")}>
        <div className={cx("min-h-[38rem] overflow-hidden rounded-ui border border-line bg-surface", view === "rules" && "min-h-0 overflow-visible border-0 bg-transparent")} role="region" aria-label={`${views.find((v) => v.id === view)!.label} view`}>
          {view === "topology" && narrow && (
            <p className="border-b border-line bg-surface-2 px-3 py-2 text-xs text-muted" role="note">
              Topology is a drag-and-connect map and works best on a larger screen. The Rules and YAML views show the same policy.
            </p>
          )}
          {view === "topology" && (
            <div className={topoMode === "configured" ? "h-[38rem]" : "h-[46rem]"}>
              <Topology doc={d.draft} services={services} hosts={hosts} management={d.management} layout={d.layout} saveLayout={d.saveLayout}
                canWrite={canWrite} selection={selection} onSelect={(s) => { setSelection(s); if (s) { setSide("details"); setPanelChoice(null); } }}
                onConnect={(from, to) => setRuleDialog({ from, to })} onNewRule={() => setRuleDialog({})} highlight={highlight} focusNodes={focus}
                mode={topoMode} onMode={setTopoMode} canObserve={can("network.read")} />
            </div>
          )}
          {view === "rules" && (
            <RulesView doc={d.draft} edit={d.edit} services={services} hosts={hosts} storedGroups={d.storedGroups} canWrite={canWrite}
              highlight={highlight.policies} fresh={suggested ? [suggested] : []} noHost={(d.check?.no_host_rules ?? []).map((r) => r.rule)} />
          )}
          {view === "yaml" && (
            <div className="h-[38rem] p-3">
              <YamlView text={d.yamlText} onChange={d.editYAML} problems={d.check?.problems ?? []} broken={d.yamlBroken} readOnly={!canWrite} />
            </div>
          )}
        </div>
        {showPanel && <aside id="designer-side" className="rounded-ui border border-line bg-surface p-3" aria-label="Details and preview">
          <Tabs label="Side panel" id="designer-side" className="mb-3" small current={side} onSelect={setSide}
            tabs={[{ key: "preview", label: "Preview" }, { key: "details", label: "Details" }]} />
          <TabPanel id="designer-side" current={side}>
            {side === "preview" ? (
              <PreviewPanel check={d.check} />
            ) : (
              <Inspector selection={selection} doc={d.draft} services={services} hosts={hosts} management={d.management}
                onEditRule={(index) => setRuleDialog({ index })} onClose={() => setSelection(null)} />
            )}
          </TabPanel>
        </aside>}
      </div>

      {ruleDialog && (
        <RuleDialog doc={d.draft} services={services} hosts={hosts} index={ruleDialog.index}
          initial={ruleDialog.from ? { from: [ruleDialog.from], to: [ruleDialog.to!] } : undefined}
          onSave={(doc) => { d.edit(doc); setRuleDialog(null); }}
          onDelete={ruleDialog.index !== undefined ? () => { d.edit(deleteRule(d.draft, ruleDialog.index!)); setRuleDialog(null); setSelection(null); } : undefined}
          onClose={() => setRuleDialog(null)} />
      )}
      {confirmSave && (
        <Dialog title="Save a policy that blocks DROOL's management path?" onClose={() => setConfirmSave(null)}>
          <div className="space-y-3 text-sm">
            <Alert>This policy would cut DROOL off from {d.management.filter((p) => p.blocked).map((p) => p.host).join(", ")}.</Alert>
            <p>You can save it as a draft of the policy, but <strong>applying it to those hosts will be refused</strong> until the conflict is fixed, or an administrator uses the audited emergency override (which still rolls back on its own if DROOL loses the host).</p>
            <div className="flex justify-end gap-2">
              <Button onClick={() => setConfirmSave(null)}>Keep editing</Button>
              <Button variant="danger" onClick={() => { const review = confirmSave.review; setConfirmSave(null); doSave(review); }}>Save anyway</Button>
            </div>
          </div>
        </Dialog>
      )}
      {applyTo && (
        <ApplyDialog hostIds={applyTo} names={applyTo.map((id) => hosts.find((h) => h.id === id)?.name ?? id)} onClose={() => setApplyTo(null)} />
      )}
    </div>
  );
}
