export class APIError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}
export async function api<T>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const response = await fetch(`/api/v1${path}`, {
    credentials: "same-origin",
    ...options,
    headers: { "Content-Type": "application/json", ...options.headers },
  });
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    throw new APIError(
      response.status,
      body?.error?.message ??
        "The server could not complete your request. Try again.",
    );
  }
  return response.status === 204 ? (undefined as T) : response.json();
}
export type Session = { email: string; expires_at: number; csrf_token: string };
export type Status = {
  ready: boolean;
  sqlite: { status: string; version: string };
  postgres: { status: string; version: string };
  versions: Record<string, string>;
  backups: string;
};
export type Settings = {
  id: string;
  hostname: string;
  origin: string;
  mode: string;
  release: string;
  caddy_version: string;
  docker_version: string;
  compose_version: string;
};
