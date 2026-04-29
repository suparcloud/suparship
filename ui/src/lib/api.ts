const BASE_URL = "/api/v1";

// __VITE_DEBUG__ is injected at build time by vite.config.ts.
// Set VITE_DEBUG=true in your .env to enable verbose API request logs.
declare const __VITE_DEBUG__: boolean;
const DEBUG =
  typeof __VITE_DEBUG__ !== "undefined" ? __VITE_DEBUG__ : false;

export class ApiError extends Error {
  constructor(
    public readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const method = options?.method ?? "GET";
  const url = `${BASE_URL}${path}`;

  // Multipart bodies must NOT carry an explicit Content-Type — the browser
  // sets it (with boundary) when it sees a FormData body. JSON bodies still
  // get the default applied below.
  const isFormData =
    typeof FormData !== "undefined" && options?.body instanceof FormData;

  if (DEBUG) {
    const body = options?.body;
    if (isFormData) {
      console.debug(`[api] --> ${method} ${url}`, "[FormData]");
    } else {
      console.debug(`[api] --> ${method} ${url}`, body ? JSON.parse(body as string) : "");
    }
  }

  const start = performance.now();
  const headers: HeadersInit = isFormData
    ? { ...options?.headers }
    : { "Content-Type": "application/json", ...options?.headers };
  const res = await fetch(url, {
    ...options,
    headers,
  });
  const elapsed = Math.round(performance.now() - start);

  if (DEBUG) {
    console.debug(`[api] <-- ${res.status} ${method} ${url} (${elapsed}ms)`);
  }

  if (!res.ok) {
    // Session expired or never set — redirect to login so the user doesn't see
    // a cryptic "not authenticated" error buried inside a component.
    if (res.status === 401 && !path.startsWith("/auth/")) {
      window.location.href = "/login";
      // Return a never-resolving promise so callers don't process a stale response.
      return new Promise<never>(() => {});
    }

    let message = res.statusText;
    try {
      const body = (await res.json()) as { error?: string };
      if (DEBUG) console.debug(`[api] error body`, body);
      message = body.error ?? message;
    } catch {
      message = await res.text().catch(() => res.statusText);
    }
    throw new ApiError(res.status, message);
  }

  if (res.status === 204) {
    return undefined as T;
  }

  const data = await res.json();
  if (DEBUG) console.debug(`[api] response`, data);
  return data as T;
}

export const api = {
  get<T>(path: string): Promise<T> {
    return request<T>(path);
  },

  post<T>(path: string, body?: unknown): Promise<T> {
    return request<T>(path, {
      method: "POST",
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
  },

  // postFormData uploads a multipart body. The browser fills in the
  // Content-Type header with the boundary; do not set it manually.
  postFormData<T>(path: string, formData: FormData): Promise<T> {
    return request<T>(path, { method: "POST", body: formData });
  },

  put<T>(path: string, body?: unknown): Promise<T> {
    return request<T>(path, {
      method: "PUT",
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
  },

  del(path: string): Promise<void> {
    return request<void>(path, { method: "DELETE" });
  },
};
