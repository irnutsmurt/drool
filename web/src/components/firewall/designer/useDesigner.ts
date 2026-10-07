import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { fk } from "../queries";
import type { Doc, MgmtPath } from "./model";

export type CheckResult = Schemas["FwDocumentCheck"];
export type Problem = Schemas["FwDocProblem"];

const docKey = ["firewall", "document"] as const;
const layoutKey = ["firewall", "layout"] as const;

export type Layout = { nodes?: Record<string, { x: number; y: number }>; expanded?: string[]; pinned?: string[] };

/**
 * The designer's single draft. Topology and Rules edit `draft` through the
 * model functions; the YAML view edits text that, once it parses, replaces
 * the draft. Every change is checked by the server (real compiler) and the
 * canonical YAML comes back from it, so all three views always show the
 * same document.
 */
export function useDesigner() {
  const qc = useQueryClient();
  const stored = useQuery({ queryKey: docKey, queryFn: () => unwrap(api.GET("/firewall/document")) });
  const [draft, setDraftState] = useState<Doc | null>(null);
  const [base, setBase] = useState("");
  const [past, setPast] = useState<Doc[]>([]);
  const [yamlText, setYamlText] = useState("");
  const [yamlEditing, setYamlEditing] = useState(false); // the YAML text holds user edits
  const [yamlBroken, setYamlBroken] = useState(false); // the YAML text doesn't parse
  const [check, setCheck] = useState<CheckResult | null>(null);
  const [checking, setChecking] = useState(false);
  const seq = useRef(0);
  const source = useRef<"model" | "yaml">("model");

  // First load (and reload after discarding).
  useEffect(() => {
    if (stored.data && draft === null) {
      setDraftState(stored.data.document as Doc);
      setBase(stored.data.revision);
      setYamlText(stored.data.yaml);
    }
  }, [stored.data, draft]);

  /** An edit from Topology or Rules. */
  const edit = useCallback((next: Doc) => {
    setDraftState((cur) => {
      if (cur && cur !== next) setPast((p) => [...p.slice(-49), cur]);
      return next;
    });
    source.current = "model";
    setYamlEditing(false);
  }, []);

  const undo = useCallback(() => {
    setPast((p) => {
      if (p.length === 0) return p;
      setDraftState(p[p.length - 1]!);
      source.current = "model";
      setYamlEditing(false);
      return p.slice(0, -1);
    });
  }, []);

  // Live check of model edits.
  useEffect(() => {
    if (!draft || source.current !== "model") return;
    const n = ++seq.current;
    setChecking(true);
    const t = setTimeout(async () => {
      try {
        const res = await unwrap(api.POST("/firewall/document/check", { body: { document: draft as Schemas["FwDocument"] } }));
        if (n !== seq.current) return;
        setCheck(res);
        setYamlText(res.yaml);
        setYamlBroken(false);
      } finally {
        if (n === seq.current) setChecking(false);
      }
    }, 300);
    return () => clearTimeout(t);
  }, [draft]);

  /** An edit in the YAML view. */
  const editYAML = useCallback((text: string) => {
    setYamlText(text);
    setYamlEditing(true);
    source.current = "yaml";
    const n = ++seq.current;
    setChecking(true);
    setTimeout(async () => {
      if (n !== seq.current) return;
      try {
        const res = await unwrap(api.POST("/firewall/document/check", { body: { yaml: text } }));
        if (n !== seq.current) return;
        setCheck(res);
        const parsed = res.yaml !== "";
        setYamlBroken(!parsed);
        if (parsed) {
          setDraftState((cur) => {
            if (cur) setPast((p) => [...p.slice(-49), cur]);
            return res.document as Doc;
          });
        }
      } finally {
        if (n === seq.current) setChecking(false);
      }
    }, 450);
  }, []);

  /** Leaving the YAML view shows the canonical form again. */
  const normalizeYAML = useCallback(() => {
    if (yamlEditing && !yamlBroken && check?.yaml) setYamlText(check.yaml);
    setYamlEditing(false);
  }, [yamlEditing, yamlBroken, check]);

  const save = useMutation({
    mutationFn: () =>
      unwrap(api.PUT("/firewall/document", {
        body: yamlEditing ? { yaml: yamlText, revision: base } : { document: draft as Schemas["FwDocument"], revision: base },
      })),
    onSuccess: (res) => {
      setDraftState(res.document as Doc);
      setBase(res.revision);
      setYamlText(res.yaml);
      setYamlEditing(false);
      setPast([]);
      source.current = "model";
      qc.setQueryData(docKey, { document: res.document, yaml: res.yaml, revision: res.revision });
      void qc.invalidateQueries({ queryKey: fk.all });
    },
  });

  const discard = useCallback(() => {
    setDraftState(null);
    setPast([]);
    setYamlEditing(false);
    setYamlBroken(false);
    source.current = "model";
    void qc.invalidateQueries({ queryKey: docKey });
  }, [qc]);

  // Layout (presentation only).
  const layout = useQuery({ queryKey: layoutKey, queryFn: async () => (await unwrap(api.GET("/firewall/layout"))) as Layout });
  const saveLayout = useMutation({
    mutationFn: (l: Layout) => unwrap(api.PUT("/firewall/layout", { body: l as Record<string, unknown> })),
    onSuccess: (_, l) => qc.setQueryData(layoutKey, l),
  });

  const dirty = !!check?.changed || yamlBroken;
  const staleBase = !!check && check.revision !== base;
  const management = useMemo(() => (check?.management ?? []) as MgmtPath[], [check]);

  const storedGroups = useMemo(() => new Set((stored.data?.document.groups ?? []).map((g) => g.name.toLowerCase())), [stored.data]);

  return {
    loading: stored.isLoading || draft === null,
    storedGroups,
    loadError: stored.error,
    draft: draft ?? { networks: [], groups: [], services: [], policy: [] },
    edit, undo, canUndo: past.length > 0, discard,
    yamlText, editYAML, normalizeYAML, yamlEditing, yamlBroken,
    check, checking, dirty, staleBase, base, management,
    save,
    layout: layout.data ?? {},
    saveLayout: saveLayout.mutate,
  };
}

export type Designer = ReturnType<typeof useDesigner>;
