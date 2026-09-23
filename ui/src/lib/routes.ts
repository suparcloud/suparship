/**
 * servicePortFromValues reads the Service port a component's chart exposes
 * from its values overlay (`service.port`, else `containerPort`). Route rules
 * forward to that port; undefined when the overlay doesn't say.
 */
export function servicePortFromValues(values: Record<string, unknown> | undefined): number | undefined {
  if (!values) return undefined;
  const svc = values.service as Record<string, unknown> | undefined;
  const port = Number(svc?.port) || Number(values.containerPort);
  return port > 0 ? port : undefined;
}

/** A route-backend candidate: a component and, when known, its Service port. */
export interface RouteComponent {
  name: string;
  port?: number;
}
