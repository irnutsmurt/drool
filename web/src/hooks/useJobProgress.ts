import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { jk } from "@/api/queries";

/** A run that hasn't ended yet. */
export const activeJob = (s?: string) => s === "pending" || s === "running" || s === "paused";

/** A run started from where the person is (DESIGN-STANDARD §5): its status
 * while it runs and each host's outcome, polled until it ends. The run's
 * own page keeps the full output and timeline. */
export function useJobProgress(jobId?: string) {
  const job = useQuery({
    queryKey: jk.job(jobId ?? "none"),
    queryFn: () => unwrap(api.GET("/jobs/{id}", { params: { path: { id: jobId! } } })),
    enabled: !!jobId,
    refetchInterval: (q) => (q.state.data && !activeJob(q.state.data.status) ? false : 1500),
  });
  const done = !!job.data && !activeJob(job.data.status);
  const hosts = useQuery({
    queryKey: jk.jobHosts(jobId ?? "none"),
    queryFn: () => unwrap(api.GET("/jobs/{id}/hosts", { params: { path: { id: jobId! } } })),
    enabled: !!jobId,
    refetchInterval: done ? false : 2500,
  });
  // Once the run has ended, its hosts' final states: never a host still
  // "running" beside a finished run.
  const { refetch } = hosts;
  useEffect(() => {
    if (done) void refetch();
  }, [done, refetch]);
  return { job: job.data, hosts: hosts.data?.items ?? [], done, error: job.error ?? hosts.error };
}
