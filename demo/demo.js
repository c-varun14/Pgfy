// Demonstration application for Pgfy: writes and reads recognizable guestbook entries.
// Usage: DATABASE_URL="postgresql://…?sslmode=verify-full" node demo.js write "Hello from my laptop"
//        DATABASE_URL="…" node demo.js read
import pg from "pg";

const [command = "read", ...rest] = process.argv.slice(2);
const url = process.env.DATABASE_URL;
if (!url) {
  console.error("Set DATABASE_URL to the connection URL shown in the Pgfy dashboard.");
  process.exit(2);
}
const client = new pg.Client({ connectionString: url, application_name: process.env.PGAPPNAME || "pgfy-demo" });
try {
  await client.connect();
  if (command === "write") {
    // Create the table only when it is missing, and only on the write path: DDL is refused while the
    // dashboard has writes frozen, and reads must keep working then.
    const { rows: existing } = await client.query("SELECT to_regclass('guestbook') AS name");
    if (!existing[0].name) await client.query("CREATE TABLE guestbook (id serial PRIMARY KEY, message text NOT NULL, written_at timestamptz NOT NULL DEFAULT now())");
    const message = rest.join(" ") || `Entry written at ${new Date().toISOString()}`;
    const { rows } = await client.query("INSERT INTO guestbook (message) VALUES ($1) RETURNING id, written_at", [message]);
    console.log(`Wrote entry #${rows[0].id} at ${rows[0].written_at.toISOString()}: ${message}`);
  } else if (command === "read") {
    const { rows } = await client.query("SELECT id, message, written_at FROM guestbook ORDER BY id");
    console.log(`${rows.length} guestbook entr${rows.length === 1 ? "y" : "ies"}:`);
    for (const row of rows) console.log(`  #${row.id}  ${row.written_at.toISOString()}  ${row.message}`);
  } else {
    console.error("Commands: write [message], read");
    process.exit(2);
  }
  const { rows } = await client.query("SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()");
  console.log(rows[0]?.ssl ? "Connected with TLS." : "Connected WITHOUT TLS.");
} catch (error) {
  // One readable line instead of a stack trace: rejected connections and frozen writes are expected outcomes.
  console.error(`Failed: ${error.message}`);
  process.exitCode = 1;
} finally {
  await client.end();
}
