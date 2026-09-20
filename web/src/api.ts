export class APIError extends Error {
  constructor(
    public status: number,
    message: string,
    public code = "",
  ) {
    super(message);
  }
}
let csrfToken = "";
/** Remembered from the session so every mutation carries the CSRF header. */
export function setCSRF(token: string) {
  csrfToken = token;
}
export async function api<T>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    ...(options.headers as Record<string, string>),
  };
  if (options.method && options.method !== "GET" && csrfToken) {
    headers["X-CSRF-Token"] ??= csrfToken;
  }
  const response = await fetch(`/api/v1${path}`, {
    credentials: "same-origin",
    ...options,
    headers,
  });
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    throw new APIError(
      response.status,
      body?.error?.message ??
        "The server could not complete your request. Try again.",
      body?.error?.code ?? "",
    );
  }
  return response.status === 204 ? (undefined as T) : response.json();
}
export type Session = {
  email: string;
  expires_at: number;
  csrf_token: string;
  client_ip?: string;
};
export type Certificate = {
  state: "placeholder" | "trusted" | "unknown";
  source?: string;
  issuer?: string;
  not_after?: string;
  fingerprint?: string;
};
export type DatabaseAccess = {
  mode: "direct" | "tunnel";
  host: string;
  port: number;
  certificate: Certificate;
};
export type Status = {
  ready: boolean;
  sqlite: { status: string; version: string };
  postgres: { status: string; version: string };
  versions: Record<string, string>;
  backups: string;
  database_access: DatabaseAccess;
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
export type PolicyState = {
  current_revision: number;
  applied_revision: number;
  state: "idle" | "pending" | "applied" | "failed";
  last_error: string;
  addresses: string[];
};
export type Connection = {
  client_addr: string;
  tls: boolean;
  application_name: string;
  since: string;
};
export type Project = {
  id: string;
  name: string;
  db_name: string;
  role_name: string;
  stage: "identity_persisted" | "role_created" | "database_created" | "ready";
  failed: boolean;
  stage_error: string;
  created_at: number;
  ready_at: number;
  size_bytes: number | null;
  size_error?: string;
  policy?: PolicyState;
  connections_now?: Connection[];
};
export type Credentials = {
  host: string;
  port: number;
  database: string;
  user: string;
  password: string;
  sslmode: string;
  url: string;
  psql: string;
};
export type ConnectionCheck = {
  id: string;
  state: "pending" | "successful" | "expired";
  expires_at: number;
  created_at: number;
  application_name?: string;
  command?: string;
  evidence?: { client_addr: string; tls: boolean; observed_at: number };
};
export type StorageSettings = {
  endpoint: string;
  region: string;
  bucket: string;
  prefix: string;
  access_key: string;
  secret_key: string;
  session_token: string;
  path_style: boolean;
};
export type CheckStep = { name: string; ok: boolean; error?: string };
export type Job = {
  id: string;
  kind: "backup" | "restore";
  project_id: string;
  state: "queued" | "running" | "succeeded" | "failed" | "interrupted";
  stage: string;
  error: string;
  result: Record<string, unknown> & {
    sha256?: string;
    size_bytes?: number;
    verified?: boolean;
    checks?: { name: string; ok: boolean; detail?: string }[];
    warnings?: string;
    project_id?: string;
  };
  created_at: number;
  started_at: number;
  finished_at: number;
  stage_at: number;
  elapsed_seconds: number;
};
export type Manifest = {
  version: number;
  installation_id: string;
  project_id: string;
  project_name: string;
  db_name: string;
  postgres_version: string;
  created_at: string;
  archive_key: string;
  sha256: string;
  size_bytes: number;
  tables: { schema: string; name: string; rows: number }[];
  manifest_key: string;
};
export type BackupRecord = {
  id: string;
  project_id: string;
  job_id: string;
  object_key: string;
  manifest: Manifest;
  size_bytes: number;
  created_at: number;
};
export { formatBytes, formatDate, formatDuration, relativeTime, STAGE_LABELS } from "./lib/format";
