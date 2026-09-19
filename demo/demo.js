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
  await client.query("CREATE TABLE IF NOT EXISTS guestbook (id serial PRIMARY KEY, message text NOT NULL, written_at timestamptz NOT NULL DEFAULT now())");
  if (command === "write") {
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
} finally {
  await client.end();
}
