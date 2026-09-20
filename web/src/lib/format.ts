export function formatBytes(bytes: number | null | undefined) {
  if (bytes == null) return "—";
  if (bytes === 0) return "Empty";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value < 10 && unit > 0 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`;
}

export function formatDate(seconds: number) {
  return new Date(seconds * 1000).toLocaleString();
}

export function formatDuration(seconds: number) {
  if (seconds < 60) return `${seconds}s`;
  return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}

export function relativeTime(seconds: number) {
  const delta = seconds - Date.now() / 1000;
  const abs = Math.abs(delta);
  const future = delta > 0;
  if (abs < 45) return future ? "in a moment" : "just now";
  let value: number;
  let unit: string;
  if (abs < 3600) { value = Math.max(1, Math.round(abs / 60)); unit = "min"; }
  else if (abs < 86400) { value = Math.max(1, Math.round(abs / 3600)); unit = "h"; }
  else if (abs < 7 * 86400) { value = Math.max(1, Math.round(abs / 86400)); unit = "d"; }
  else return new Date(seconds * 1000).toLocaleDateString(undefined, { day: "numeric", month: "short" });
  return future ? `in ${value} ${unit}` : `${value} ${unit} ago`;
}

export function formatDateTime(seconds: number) {
  return new Date(seconds * 1000).toLocaleString(undefined, { day: "numeric", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit" });
}

export const STAGE_LABELS: Record<string, string> = {
  queued: "Waiting for its turn",
  starting: "Starting",
  preparing: "Checking storage and disk space",
  snapshot: "Taking a consistent snapshot",
  dump: "Exporting the database",
  checksum: "Computing checksum",
  upload_archive: "Uploading archive",
  upload_manifest: "Publishing manifest",
  download: "Downloading archive",
  verify_archive: "Verifying archive checksum",
  create_project: "Creating the new database",
  restore: "Restoring into the new database",
  verify: "Verifying restored data",
  done: "Done",
};
