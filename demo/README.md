# Pgfy demo guestbook

A deliberately small application that writes and reads recognizable rows, used to show a live
connection, a backup, and a recovery on a replacement server.

```sh
cd demo && npm install
export DATABASE_URL='postgresql://app_…:…@db.example.com:5432/app_…?sslmode=verify-full'   # from the dashboard
node demo.js write "Order #1001 from the shop"
node demo.js read
```

`sslmode=verify-full` checks the server certificate against your system's trusted authorities (the Node driver uses them automatically; `psql` needs `sslrootcert=system` added).
Through an SSH tunnel use the URL the dashboard shows for tunnel mode (`sslmode=disable`; the tunnel encrypts the hop).
