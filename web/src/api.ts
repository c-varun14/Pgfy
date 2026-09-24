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
export type HostDisk = { name: "postgres" | "workspace" | "root"; device?: number; total_bytes?: number; free_bytes?: number; free_percent: number; low: boolean; error?: string };
export type HostStatus = {
  /** ok, stale (the host timer stopped writing) or unknown. */
  state: "ok" | "stale" | "unknown";
  written_at: number;
  disks: HostDisk[];
  ntp_synchronized: boolean | null;
  certificate: { state: string; issuer?: string; expires_at: number | null; expiring: boolean; last_sync?: { at: string; ok: boolean; message: string } };
};
export type Status = {
  ready: boolean;
  maintenance?: boolean;
  host?: HostStatus;
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
  /** Unix seconds since application writes were frozen; 0 while writes are allowed. */
  frozen_at: number;
  size_bytes: number | null;
  size_error?: string;
  policy?: PolicyState;
  connections_now?: Connection[];
  /** The access policy admits any address: TLS and the password are the only protection. */
  open_to_internet?: boolean;
  /** A password change was recorded but not yet confirmed by PostgreSQL. */
  rotation_pending?: boolean;
  limits?: ProjectLimits;
};
export type Limits = {
  statement_timeout_ms: number;
  idle_in_transaction_ms: number;
  /** -1 is unlimited. */
  temp_file_limit_kb: number;
  lock_timeout_ms: number;
  connection_limit: number;
};
export type ProjectLimits = Limits & { revision: number; applied_revision: number; last_error?: string };
export type RoleUse = { role: string; project?: string; limit: number; connections: number; warning: boolean };
export type ConnectionBudget = {
  max_connections: number;
  superuser_reserved: number;
  reserved: number;
  available: number;
  projects_used: number;
  projects_limit: number;
  other_used: number;
  warning: boolean;
  overcommitted: boolean;
  roles: RoleUse[];
  system: RoleUse[];
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
  rotation_pending?: boolean;
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
  private_endpoint: boolean;
  /** How the bucket is protected against a deleted backup being unrecoverable. */
  bucket_protection: "versioning" | "acknowledged" | "";
  protection_state?: "enabled" | "disabled" | "unsupported" | "";
  protection_checked_at?: number;
};
export type BackupPolicy = {
  target_interval_hours: number;
  retention_daily: number;
  retention_weekly: number;
  updated_at: number;
};
export type CheckStep = { name: string; ok: boolean; error?: string; detail?: string };
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
    verification?: "verified" | "partial" | "failed";
    summary?: string;
    restore_errors?: number;
    stderr?: string;
    tables_truncated?: boolean;
    checks?: { name: string; ok: boolean; detail?: string }[];
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
  tables_truncated?: boolean;
  manifest_key: string;
};
/** One backup as the bucket currently holds it. */
export type BucketBackup = {
  manifest_key: string;
  archive_key: string;
  db_name: string;
  taken_at: number;
  state: "complete" | "manifest_only" | "archive_only" | "damaged";
  installation_id: string;
  project_id: string;
  project_name: string;
  postgres_version: string;
  table_count: number;
  size_bytes: number;
};
/** Every backup of one database, paged on its own so none can be hidden. */
export type DatabaseBackups = {
  db_name: string;
  project_id?: string;
  project_name?: string;
  installation_id?: string;
  mixed: boolean;
  foreign: boolean;
  newest_at: number;
  count: number;
  total_bytes: number;
  has_more: boolean;
  manifest_only: number;
  damaged: number;
  reconciled_at: number;
  backups: BucketBackup[];
};
export type Discovery = {
  state: "ok" | "checking" | "storage_not_configured";
  databases: DatabaseBackups[];
  installation_id?: string;
  reconciled_at?: number;
  storage_error?: string;
  busy?: boolean;
  restores?: Job[];
};
export type BackupHistory = {
  backups: BackupRecord[];
  jobs: Job[];
  storage_configured: boolean;
  next_scheduled_at: number;
  newest_backup_at: number;
  target_interval_hours: number;
  failures: number;
  last_attempt_at: number;
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
