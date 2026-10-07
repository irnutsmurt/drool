import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { useHost } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Checkbox } from "@/components/ui/Checkbox";
import { Dialog } from "@/components/ui/Dialog";
import { Field, Input } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { cx } from "@/components/ui/cx";
import { errorMessage, formatBytes, formatDateTime } from "@/lib/format";
import { brief, describe, octal, parseOctal, symbolic } from "@/lib/modes";
import { upload } from "@/lib/upload";

type Entry = Schemas["FileInfo"];
type Listing = Schemas["FileListing"];

const join = (dir: string, name: string) => (dir === "/" ? "/" + name : dir + "/" + name);
const parentOf = (p: string) => p.replace(/\/[^/]*$/, "") || "/";

/** Resolve a symlink target relative to the link's directory. */
function resolveTarget(dir: string, target: string): string {
  const parts = (target.startsWith("/") ? target : dir + "/" + target).split("/");
  const out: string[] = [];
  for (const p of parts) {
    if (!p || p === ".") continue;
    if (p === "..") out.pop();
    else out.push(p);
  }
  return "/" + out.join("/");
}

function kindIcon(e: Entry) {
  return { dir: "📁", file: "📄", symlink: "🔗", other: "◻" }[e.kind];
}

type Action = { kind: "rename" | "chmod" | "chown" | "delete"; entry: Entry } | { kind: "mkdir" } | null;

export function FilesPage() {
  const { id = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { can } = useAuth();
  const host = useHost(id);
  const path = params.get("path") ?? "";
  const elevated = params.get("elevated") === "1";
  const canWrite = can("files.write");
  const [jump, setJump] = useState("");
  const [action, setAction] = useState<Action>(null);
  const [uploading, setUploading] = useState<{ name: string; progress: number } | null>(null);
  const [notice, setNotice] = useState<string>();
  const fileInput = useRef<HTMLInputElement>(null);
  const abortRef = useRef<AbortController | null>(null);

  const listing = useQuery({
    queryKey: ["files", id, path, elevated],
    queryFn: () => unwrap(api.GET("/hosts/{id}/files", { params: { path: { id }, query: { path: path || undefined, elevated } } })),
    retry: false,
  });
  const dir = listing.data?.path ?? path;
  const go = (p: string, extra?: Record<string, string>) =>
    setParams({ path: p, ...(elevated ? { elevated: "1" } : {}), ...extra });
  const refresh = () => void qc.invalidateQueries({ queryKey: ["files", id] });

  function setElevated(on: boolean) {
    setParams({ ...(dir ? { path: dir } : {}), ...(on ? { elevated: "1" } : {}) });
  }

  async function onUpload(files: FileList | null) {
    if (!files?.length) return;
    setNotice(undefined);
    for (const f of Array.from(files)) {
      const dest = join(dir, f.name);
      const exists = listing.data?.entries.some((e) => e.name === f.name);
      if (exists && !confirm(`${f.name} already exists in ${dir}. Replace it?`)) continue;
      const ctl = new AbortController();
      abortRef.current = ctl;
      setUploading({ name: f.name, progress: 0 });
      try {
        await upload("PUT", `/hosts/${id}/files/content?path=${encodeURIComponent(dest)}&overwrite=${exists ? "true" : "false"}&elevated=${elevated}`,
          f, (p) => setUploading({ name: f.name, progress: p }), ctl.signal);
        setNotice(`Uploaded ${f.name} (${formatBytes(f.size)}).`);
      } catch (e) {
        setNotice(`${f.name}: ${errorMessage(e)}`);
      }
    }
    setUploading(null);
    if (fileInput.current) fileInput.current.value = "";
    refresh();
  }

  function open(e: Entry) {
    if (e.kind === "dir") return go(e.path);
    if (e.kind === "symlink" && e.target) {
      // Follow the link explicitly: open the target's real location.
      const t = resolveTarget(dir, e.target);
      return go(t);
    }
    if (e.kind === "file") {
      navigate(`/hosts/${id}/files/edit?path=${encodeURIComponent(e.path)}${elevated ? "&elevated=1" : ""}`);
    }
  }

  const err = listing.error;
  // A symlink to a file: listing its target fails with "not a directory";
  // show the file's directory instead.
  const notDir = !!err && /not a directory/.test(errorMessage(err)) && !!path;
  useEffect(() => {
    if (notDir) go(parentOf(path));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [notDir, path]);
  const crumbs = dir.split("/").filter(Boolean);
  const caps = listing.data?.caps;

  return (
    <>
      <PageHeader
        title={`Files on ${host.data?.name ?? ""}`}
        description={host.data && <Link className="text-accent" to={`/hosts/${id}`}>Back to host</Link>}
        actions={can("files.elevate") && (
          <Checkbox checked={elevated} onChange={setElevated} label="Use administrator privileges"
            hint="Act as root through this host's sudo setting." />
        )}
      />
      {elevated && (
        <div className="mb-4">
          <Alert tone="warn">
            Administrator privileges are on: you are reading and changing files as <strong>root</strong>. Every change is
            recorded in the audit log.
          </Alert>
        </div>
      )}
      {!elevated && caps?.root_equivalent && (
        <div className="mb-4"><Alert tone="warn">This host's login account is root: every change is made as root.</Alert></div>
      )}

      <Card>
        <div className="mb-3 flex flex-wrap items-center gap-2">
          <Button size="sm" onClick={() => go(parentOf(dir))} disabled={dir === "/"} title="Up one level">↑ Up</Button>
          <nav aria-label="Current path" className="flex min-w-0 flex-1 flex-wrap items-center font-mono text-sm">
            <button className="text-accent" onClick={() => go("/")}>/</button>
            {crumbs.map((c, i) => (
              <span key={i} className="flex items-center">
                <button className="text-accent" onClick={() => go("/" + crumbs.slice(0, i + 1).join("/"))}>{c}</button>
                {i < crumbs.length - 1 && <span className="px-0.5 text-muted">/</span>}
              </span>
            ))}
          </nav>
          <form className="flex gap-2" onSubmit={(e: FormEvent) => { e.preventDefault(); if (jump.trim()) go(jump.trim()); setJump(""); }}>
            <Input className="h-7 w-56 font-mono text-xs" placeholder="Go to path…" value={jump} onChange={(e) => setJump(e.target.value)} />
          </form>
          <Button size="sm" onClick={refresh}>Refresh</Button>
          {canWrite && (
            <>
              <Button size="sm" onClick={() => setAction({ kind: "mkdir" })}>New folder</Button>
              <Button size="sm" variant="primary" onClick={() => fileInput.current?.click()} disabled={!!uploading}>Upload…</Button>
              <input ref={fileInput} type="file" multiple className="hidden" onChange={(e) => onUpload(e.target.files)} />
            </>
          )}
        </div>
        {uploading && (
          <div className="mb-3 flex items-center gap-3 text-sm">
            <span>Uploading {uploading.name}…</span>
            <div className="h-2 w-48 overflow-hidden rounded bg-surface-2"><div className="h-full bg-accent" style={{ width: `${Math.round(uploading.progress * 100)}%` }} /></div>
            <span className="text-muted">{Math.round(uploading.progress * 100)}%</span>
            <Button size="sm" onClick={() => abortRef.current?.abort()}>Cancel</Button>
          </div>
        )}
        {notice && <div className="mb-3"><Alert tone={/:/.test(notice) && !notice.startsWith("Uploaded") ? "danger" : "ok"}>{notice}</Alert></div>}
        {err && !/not a directory/.test(errorMessage(err)) && <Alert>{errorMessage(err)}</Alert>}
        {listing.isPending && <p className="text-sm text-muted">Loading…</p>}
        {listing.data && (
          <FileTable entries={listing.data.entries} canWrite={canWrite} elevated={elevated} hostId={id}
            onOpen={open} onAction={setAction} />
        )}
      </Card>

      {action && (
        <ActionDialog action={action} hostId={id} dir={dir} elevated={elevated}
          onClose={() => setAction(null)} onDone={(msg) => { setAction(null); setNotice(msg); refresh(); }} />
      )}
    </>
  );
}

function FileTable({ entries, canWrite, elevated, hostId, onOpen, onAction }: {
  entries: Listing["entries"];
  canWrite: boolean;
  elevated: boolean;
  hostId: string;
  onOpen: (e: Entry) => void;
  onAction: (a: Action) => void;
}) {
  if (entries.length === 0) return <p className="text-sm text-muted">This directory is empty.</p>;
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead className="text-left text-xs uppercase text-muted">
          <tr>
            <th className="py-2 pr-3">Name</th>
            <th className="py-2 pr-3 text-right">Size</th>
            <th className="py-2 pr-3">Owner</th>
            <th className="py-2 pr-3">Permissions</th>
            <th className="py-2 pr-3">Modified</th>
            <th className="py-2" />
          </tr>
        </thead>
        <tbody className="divide-y divide-line">
          {entries.map((e) => (
            <tr key={e.name} className="group">
              <td className="max-w-md py-1.5 pr-3">
                <button className={cx("text-left", e.kind !== "other" && "text-accent hover:underline")} onClick={() => onOpen(e)}
                  title={e.kind === "file" ? "View / edit" : e.kind === "symlink" ? `Go to ${e.target}` : undefined}>
                  <span className="mr-1.5">{kindIcon(e)}</span>
                  <span className="font-mono">{e.name}</span>
                </button>
                {e.kind === "symlink" && <span className="ml-2 font-mono text-xs text-muted">→ {e.target}</span>}
              </td>
              <td className="whitespace-nowrap py-1.5 pr-3 text-right tabular-nums text-muted">{e.kind === "file" ? formatBytes(e.size) : ""}</td>
              <td className="py-1.5 pr-3 font-mono text-xs">{e.owner}:{e.group}</td>
              {e.kind === "symlink" ? (
                <td className="py-1.5 pr-3 text-xs text-muted">link (the target's permissions apply)</td>
              ) : (
                <td className="py-1.5 pr-3" title={describe(e.mode, e.kind === "dir")}>
                  <span className="font-mono text-xs">{symbolic(e.mode)}</span>
                  <span className="ml-1.5 font-mono text-xs text-muted">{octal(e.mode)}</span>
                  <span className="block whitespace-nowrap text-xs text-muted">{brief(e.mode, e.kind === "dir")}</span>
                </td>
              )}
              <td className="whitespace-nowrap py-1.5 pr-3 text-muted">{formatDateTime(e.mtime)}</td>
              <td className="whitespace-nowrap py-1.5 text-right">
                <span className="inline-flex gap-1">
                  {e.kind === "file" && (
                    <a className="inline-flex h-7 items-center rounded-ui px-2 text-xs text-accent hover:bg-surface-2"
                      href={`/api/v1/hosts/${hostId}/files/content?path=${encodeURIComponent(e.path)}&elevated=${elevated}`} download>
                      Download
                    </a>
                  )}
                  {canWrite && (
                    <>
                      <Button size="sm" variant="ghost" title="Rename or move" onClick={() => onAction({ kind: "rename", entry: e })}>Rename</Button>
                      {e.kind !== "symlink" && <Button size="sm" variant="ghost" onClick={() => onAction({ kind: "chmod", entry: e })}>Permissions</Button>}
                      <Button size="sm" variant="ghost" onClick={() => onAction({ kind: "chown", entry: e })}>Owner</Button>
                      <Button size="sm" variant="ghost" className="text-danger" onClick={() => onAction({ kind: "delete", entry: e })}>Delete</Button>
                    </>
                  )}
                </span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ActionDialog({ action, hostId, dir, elevated, onClose, onDone }: {
  action: NonNullable<Action>;
  hostId: string;
  dir: string;
  elevated: boolean;
  onClose: () => void;
  onDone: (msg: string) => void;
}) {
  const entry = "entry" in action ? action.entry : undefined;
  const [name, setName] = useState(action.kind === "rename" ? entry!.path : "");
  const [mode, setMode] = useState(entry ? octal(entry.mode) : "0755");
  const [owner, setOwner] = useState(entry?.owner ?? "");
  const [group, setGroup] = useState(entry?.group ?? "");
  const [recursive, setRecursive] = useState(false);
  const p = { params: { path: { id: hostId } } };
  const run = useMutation({
    mutationFn: async () => {
      switch (action.kind) {
        case "mkdir":
          await unwrap(api.POST("/hosts/{id}/files/mkdir", { ...p, body: { path: join(dir, name.trim()), mode, elevated } }));
          return `Created ${name.trim()}.`;
        case "rename":
          await unwrap(api.POST("/hosts/{id}/files/rename", { ...p, body: { path: entry!.path, to: name.trim().startsWith("/") ? name.trim() : join(dir, name.trim()), elevated } }));
          return `Moved ${entry!.name} to ${name.trim()}.`;
        case "chmod":
          await unwrap(api.POST("/hosts/{id}/files/chmod", { ...p, body: { path: entry!.path, mode, elevated } }));
          return `Changed the permissions of ${entry!.name} to ${mode}.`;
        case "chown":
          await unwrap(api.POST("/hosts/{id}/files/chown", { ...p, body: { path: entry!.path, owner: owner.trim(), group: group.trim(), elevated } }));
          return `Changed the owner of ${entry!.name} to ${owner}:${group}.`;
        case "delete":
          await unwrap(api.POST("/hosts/{id}/files/delete", { ...p, body: { path: entry!.path, recursive, elevated } }));
          return `Deleted ${entry!.name}.`;
      }
    },
    onSuccess: (msg) => onDone(msg ?? ""),
  });
  const parsed = parseOctal(mode);
  const isDir = entry?.kind === "dir" || action.kind === "mkdir";
  const title = { mkdir: "New folder", rename: `Rename or move ${entry?.name}`, chmod: `Permissions of ${entry?.name}`,
    chown: `Owner of ${entry?.name}`, delete: `Delete ${entry?.name}?` }[action.kind];

  return (
    <Dialog title={title} onClose={onClose}>
      <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); run.mutate(); }}>
        {run.error && <Alert>{errorMessage(run.error)}</Alert>}
        {action.kind === "mkdir" && (
          <Field label="Name"><Input autoFocus value={name} required onChange={(e) => setName(e.target.value)} /></Field>
        )}
        {action.kind === "rename" && (
          <Field label="New path" hint="A new name, or a full path to move it to another directory. Existing files are never replaced.">
            <Input autoFocus className="font-mono" value={name} required onChange={(e) => setName(e.target.value)} />
          </Field>
        )}
        {(action.kind === "chmod" || action.kind === "mkdir") && (
          <ModeEditor mode={mode} setMode={setMode} isDir={isDir} />
        )}
        {action.kind === "chown" && (
          <div className="grid grid-cols-2 gap-3">
            <Field label="Owner (user)"><Input value={owner} onChange={(e) => setOwner(e.target.value)} /></Field>
            <Field label="Group"><Input value={group} onChange={(e) => setGroup(e.target.value)} /></Field>
            <p className="col-span-2 text-xs text-muted">Changing the owner usually needs administrator privileges.</p>
          </div>
        )}
        {action.kind === "delete" && (
          <div className="space-y-3 text-sm">
            <p>
              <span className="font-mono">{entry!.path}</span> will be deleted on the host. This can't be undone.
              {entry!.kind === "symlink" && " Only the link is removed, not what it points to."}
            </p>
            {entry!.kind === "dir" && (
              <Checkbox checked={recursive} onChange={setRecursive} label="Also delete everything inside it"
                hint="Without this, only an empty folder can be deleted." />
            )}
          </div>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>Cancel</Button>
          <Button type="submit" variant={action.kind === "delete" ? "danger" : "primary"}
            disabled={run.isPending || ((action.kind === "chmod" || action.kind === "mkdir") && parsed == null)}>
            {action.kind === "delete" ? "Delete" : "Apply"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** Checkbox grid plus octal, always with a plain-language description. */
export function ModeEditor({ mode, setMode, isDir }: { mode: string; setMode: (m: string) => void; isDir: boolean }) {
  const v = parseOctal(mode);
  const toggle = (bit: number) => v != null && setMode(octal(v ^ bit));
  const labels = isDir ? ["List", "Add/remove", "Enter"] : ["Read", "Write", "Run"];
  return (
    <div className="space-y-2">
      <table className="text-sm">
        <thead><tr><th /><th className="px-2 text-xs font-medium text-muted">{labels[0]}</th><th className="px-2 text-xs font-medium text-muted">{labels[1]}</th><th className="px-2 text-xs font-medium text-muted">{labels[2]}</th></tr></thead>
        <tbody>
          {["Owner", "Group", "Everyone else"].map((w, i) => (
            <tr key={w}>
              <td className="pr-3">{w}</td>
              {[4, 2, 1].map((b) => {
                const bit = b << ((2 - i) * 3);
                return (
                  <td key={b} className="px-2 text-center">
                    <input type="checkbox" aria-label={`${w} ${labels[[4, 2, 1].indexOf(b)]}`} disabled={v == null}
                      checked={v != null && (v & bit) !== 0} onChange={() => toggle(bit)} />
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
      <Field label="Numeric mode" hint={v == null ? "Use octal, like 644 or 0755." : describe(v, isDir)}>
        <Input className="w-28 font-mono" value={mode} onChange={(e) => setMode(e.target.value)} />
      </Field>
    </div>
  );
}
