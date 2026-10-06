import { useEffect, useRef } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { jk } from "@/api/queries";

export interface OutputEvent {
  host_id: string;
  step: number;
  seq: number;
  stream: "stdout" | "stderr";
  text: string;
}

type OutputListener = (ev: OutputEvent) => void;

/**
 * Subscribes to a job's server-sent events while the job is active. Job and
 * host events refresh the cached queries (throttled); output events go to
 * registered listeners. "reset" means events were missed, so everything is
 * refetched.
 */
export function useJobEvents(jobId: string, active: boolean) {
  const qc = useQueryClient();
  const listeners = useRef(new Set<OutputListener>());

  useEffect(() => {
    if (!active) return;
    const es = new EventSource(`/api/v1/jobs/${jobId}/events`);
    let timer: ReturnType<typeof setTimeout> | undefined;
    const refresh = () => {
      if (timer) return;
      timer = setTimeout(() => {
        timer = undefined;
        void qc.invalidateQueries({ queryKey: jk.job(jobId) });
      }, 300);
    };
    es.addEventListener("job", refresh);
    es.addEventListener("host", refresh);
    es.addEventListener("output", (e) => {
      const ev = JSON.parse((e as MessageEvent).data) as OutputEvent;
      listeners.current.forEach((l) => l(ev));
    });
    es.addEventListener("reset", () => {
      void qc.invalidateQueries({ queryKey: jk.job(jobId) });
      listeners.current.forEach((l) => l({ host_id: "", step: -1, seq: -1, stream: "stdout", text: "" }));
    });
    es.addEventListener("end", () => {
      es.close();
      void qc.invalidateQueries({ queryKey: jk.job(jobId) });
      void qc.invalidateQueries({ queryKey: jk.jobs });
    });
    return () => {
      es.close();
      if (timer) clearTimeout(timer);
    };
  }, [jobId, active, qc]);

  return {
    onOutput(l: OutputListener) {
      listeners.current.add(l);
      return () => listeners.current.delete(l);
    },
  };
}
