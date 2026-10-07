import { useEffect, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { api, ApiError, unwrap } from "@/api/client";
import { useHost } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { PageHeader } from "@/components/ui/PageHeader";
import { errorMessage, formatBytes, formatDateTime } from "@/lib/format";
import { describe, octal, symbolic } from "@/lib/modes";

/** View and edit one UTF-8 text file. Not an IDE. */
export function EditFilePage() {
  const { id = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const path = params.get("path") ?? "";
  const elevated = params.get("elevated") === "1";
  const { can } = useAuth();
  const host = useHost(id);
  const dir = path.replace(/\/[^/]*$/, "") || "/";
  const back = `/hosts/${id}/files?path=${encodeURIComponent(dir)}${elevated ? "&elevated=1" : ""}`;

  const file = useQuery({
    queryKey: ["file-text", id, path, elevated],
    queryFn: () => unwrap(api.GET("/hosts/{id}/files/text", { params: { path: { id }, query: { path, elevated } } })),
    retry: false,
    refetchOnWindowFocus: false,
  });
  const [text, setText] = useState("");
  const [base, setBase] = useState<string>(); // sha256 of the version being edited
  const [conflict, setConflict] = useState(false);
  const [saved, setSaved] = useState<string>();
  useEffect(() => {
    if (file.data) {
      setText(file.data.content);
      setBase(file.data.sha256);
      setConflict(false);
    }
  }, [file.data]);
  const dirty = file.data != null && text !== file.data.content;
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const save = useMutation({
    mutationFn: ({ force, elevate }: { force: boolean; elevate?: boolean }) => unwrap(api.PUT("/hosts/{id}/files/text", {
      params: { path: { id } }, body: { path, content: text, elevated: elevated || !!elevate, expect_sha256: base, force },
    })),
    onSuccess: (r, { elevate }) => {
      setBase(r.sha256);
      setConflict(false);
      setSaved(`Saved at ${new Date().toLocaleTimeString()}${elevate ? " as root" : ""}.`);
      if (elevate && !elevated) setParams({ path, elevated: "1" });
      else file.refetch();
    },
    onError: (e) => {
      if (e instanceof ApiError && e.problem?.code === "file_changed") setConflict(true);
    },
  });

  const info = file.data?.info;
  const denied = !elevated && can("files.elevate") && save.error instanceof ApiError && save.error.problem?.code === "permission_denied";
  const notText = file.error instanceof ApiError && file.error.problem?.code === "not_text";
  return (
    <>
      <PageHeader title={path.split("/").pop() || path}
        description={<span className="font-mono">{host.data?.name}:{path}</span>}
        actions={<Link className="text-sm text-accent" to={back}>Back to {dir}</Link>} />
      {elevated && <div className="mb-4"><Alert tone="warn">Editing as <strong>root</strong> (administrator privileges). The save is audited.</Alert></div>}
      {file.error && (
        <Alert tone={notText ? "warn" : "danger"}>
          {errorMessage(file.error)}
          {notText && (
            <div className="mt-2">
              <a className="text-accent underline" download
                href={`/api/v1/hosts/${id}/files/content?path=${encodeURIComponent(path)}&elevated=${elevated}`}>Download it</a>
            </div>
          )}
        </Alert>
      )}
      {file.data && info && (
        <Card>
          <div className="mb-3 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted">
            <span>{formatBytes(info.size)}</span>
            <span className="font-mono">{info.owner}:{info.group}</span>
            <span title={describe(info.mode)}><span className="font-mono">{symbolic(info.mode)} ({octal(info.mode)})</span></span>
            <span>modified {formatDateTime(info.mtime)}</span>
            <span className="font-mono" title="SHA-256 of the version you opened">sha256 {base?.slice(0, 12)}</span>
          </div>
          {conflict && (
            <div className="mb-3">
              <Alert tone="danger">
                This file changed on the host after you opened it. Reload it or explicitly overwrite the newer version.
                <div className="mt-2 flex gap-2">
                  <Button size="sm" onClick={() => { if (!dirty || confirm("Discard your changes and load the current version?")) file.refetch(); }}>
                    Reload (discard my changes)
                  </Button>
                  <Button size="sm" variant="danger" onClick={() => save.mutate({ force: true })}>Overwrite the newer version</Button>
                </div>
              </Alert>
            </div>
          )}
          {save.error && !conflict && (
            <div className="mb-3">
              <Alert>
                {errorMessage(save.error)}
                {denied && (
                  <div className="mt-2">
                    <Button size="sm" onClick={() => save.mutate({ force: false, elevate: true })}>Save with administrator privileges</Button>
                    <span className="ml-2 text-xs text-muted">Writes the file as root through this host's sudo setting.</span>
                  </div>
                )}
              </Alert>
            </div>
          )}
          {saved && !dirty && !save.error && <div className="mb-3"><Alert tone="ok">{saved}</Alert></div>}
          <textarea aria-label="File content" spellCheck={false} readOnly={!can("files.write")}
            className="h-[60vh] w-full resize-y rounded-ui border border-line bg-surface p-3 font-mono text-xs leading-5"
            value={text} onChange={(e) => { setText(e.target.value); setSaved(undefined); }} />
          {can("files.write") && (
            <div className="mt-3 flex items-center gap-2">
              <Button variant="primary" disabled={!dirty || save.isPending} onClick={() => save.mutate({ force: false })}>
                {save.isPending ? "Saving…" : "Save"}
              </Button>
              <Button disabled={!dirty} onClick={() => setText(file.data!.content)}>Undo changes</Button>
              {dirty && <span className="text-xs text-muted">Unsaved changes</span>}
            </div>
          )}
        </Card>
      )}
    </>
  );
}
