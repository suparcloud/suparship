import { useEffect, useRef } from "react";

type Freshness = { state: string; observedAt: string; revision: string };
export const watchedReadPaths = new Set<string>();
export const readStates = new Map<string, Freshness>();
const notify = () => window.dispatchEvent(new Event("read-freshness"));
export function recordReadState(path: string, headers: Headers) {
  const state = headers.get("X-Read-State");
  if (!state) return;
  readStates.set(path.split("?")[0] ?? path, {
    state, observedAt: headers.get("X-Read-Observed-At") ?? "",
    revision: headers.get("X-Read-Revision") ?? "",
  });
  notify();
}

let streams = 0;
// Only refresh the supplied read-only state. Editors retain their own drafts.
// Cap streams to leave HTTP/1.1 browser connections available for mutations.
export function useReadView(path: string | undefined, refresh: (isCurrent: () => boolean) => Promise<unknown>, poll = false) {
  const callback = useRef(refresh);
  callback.current = refresh;
  useEffect(() => {
    if (!path) return;
    watchedReadPaths.add(path);
    let cancelled = false;
    let busy = false;
    let revision = readStates.get(path)?.revision;
    const update = async () => {
      if (busy || cancelled || document.hidden) return false;
      busy = true;
      try { await callback.current(() => !cancelled); return true; } catch {
        const previous = readStates.get(path);
        if (!cancelled && previous) { readStates.set(path, { ...previous, state: "stale" }); notify(); }
        return false;
      }
      finally { busy = false; }
    };
    let source: EventSource | undefined;
    let timer: ReturnType<typeof setInterval> | undefined;
    if (!poll && streams < 3) {
      streams++;
      source = new EventSource(`/api/v1${path}?watch=1`);
      source.addEventListener("view", (event: MessageEvent<string>) => {
        const state = JSON.parse(event.data) as Freshness;
        readStates.set(path, state);
        notify();
        if (state.revision && state.revision !== revision) {
          void update().then(success => { if (success) revision = state.revision; });
        }
      });
      // The server deliberately closes streams for reauthorization. Age-based
      // freshness detects real outages without flashing stale on each reconnect.
      source.onerror = notify;
    } else {
      timer = setInterval(() => void update(), 5000);
    }
    const visible = () => { if (!document.hidden) { revision = undefined; void update(); } };
    document.addEventListener("visibilitychange", visible);
    return () => {
      cancelled = true;
      if (source) { source.close(); streams--; }
      if (timer) clearInterval(timer);
      document.removeEventListener("visibilitychange", visible);
      watchedReadPaths.delete(path);
      readStates.delete(path);
      notify();
    };
  }, [path, poll]);
}
