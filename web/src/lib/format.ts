export function formatBytes(bytes: number | null | undefined) {
  if (bytes == null) return "—";
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
  const [value, unit] = abs < 60 ? [Math.max(1, Math.round(abs)), "min"] : abs < 86400 ? [Math.round(abs / 3600), "h"] : [Math.round(abs / 86400), "d"];
  return delta > 0 ? `in ${value} ${unit}` : `${value} ${unit} ago`;
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
