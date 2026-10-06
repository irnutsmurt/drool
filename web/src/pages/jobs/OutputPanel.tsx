import { useEffect, useRef, useState } from "react";
import { api, unwrap, type Schemas } from "@/api/client";
import type { OutputEvent } from "@/hooks/useJobEvents";
import { Button } from "@/components/ui/Button";
import { cx } from "@/components/ui/cx";

interface Segment {
  seq: number;
  stream: "stdout" | "stderr";
  text: string;
}

/**
 * Output of one host step: loads stored segments, then appends live output
 * events. stderr is tinted; a reset refetches from the start.
 */
export function OutputPanel({ jobId, hostId, step, live, onOutput, truncated }: {
  jobId: string;
  hostId: string;
  step: number;
  live: boolean;
  onOutput: (l: (ev: OutputEvent) => void) => () => void;
  truncated?: boolean;
}) {
  const [segments, setSegments] = useState<Segment[]>([]);
  const [error, setError] = useState<string>();
  const lastSeq = useRef(0);
  const box = useRef<HTMLPreElement>(null);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    let cancelled = false;
    lastSeq.current = 0;
    setSegments([]);
    (async () => {
      let after = 0;
      for (;;) {
        // The endpoint also serves text/plain downloads; JSON is the default.
        const page = (await unwrap(api.GET("/jobs/{id}/hosts/{hostId}/output", {
          params: { path: { id: jobId, hostId }, query: { step, after } },
        }))) as Schemas["JobOutput"];
        if (cancelled) return;
        const fresh = page.segments.filter((s) => s.seq > lastSeq.current);
        if (fresh.length) {
          lastSeq.current = fresh[fresh.length - 1]!.seq;
          setSegments((prev) => [...prev, ...fresh]);
        }
        if (!page.more) break;
        after = page.next_seq;
      }
    })().catch((e) => setError(String(e)));
    return () => {
      cancelled = true;
    };
  }, [jobId, hostId, step, reload]);

  useEffect(() => {
    if (!live) return;
    return onOutput((ev) => {
      if (ev.seq === -1) return setReload((n) => n + 1); // reset
      if (ev.host_id !== hostId || ev.step !== step || ev.seq <= lastSeq.current) return;
      lastSeq.current = ev.seq;
      setSegments((prev) => [...prev, ev]);
    });
  }, [live, onOutput, hostId, step]);

  useEffect(() => {
    const el = box.current;
    if (el && el.scrollHeight - el.scrollTop - el.clientHeight < 80) el.scrollTop = el.scrollHeight;
  }, [segments]);

  return (
    <div className="space-y-2">
      {error && <p className="text-sm text-danger">{error}</p>}
      <pre ref={box} className="max-h-[28rem] min-h-24 overflow-auto whitespace-pre-wrap break-all rounded-ui bg-surface-2 p-3 font-mono text-xs">
        {segments.length === 0 ? <span className="text-muted">{live ? "Waiting for output…" : "No output."}</span> : segments.map((s) => (
          <span key={s.seq} className={cx(s.stream === "stderr" && "text-warn")}>{s.text}</span>
        ))}
      </pre>
      <div className="flex items-center gap-3 text-xs text-muted">
        <span>stderr is highlighted.</span>
        {truncated && <span className="text-warn">Output was truncated at the size limit.</span>}
        <a className="ml-auto" href={`/api/v1/jobs/${jobId}/hosts/${hostId}/output?step=${step}&format=text`}>
          <Button type="button" size="sm">Download</Button>
        </a>
      </div>
    </div>
  );
}
