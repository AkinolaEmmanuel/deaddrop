# DeadDrop

DeadDrop is a small command-line tool for transferring a file between two peers
over a WebSocket connection with **end-to-end encryption**. The file is
encrypted on the sender's machine and only decrypted on the receiver's machine —
the bytes on the wire are AES-256-GCM ciphertext, and the encryption key is
never transmitted.

Two peers agree out-of-band on a **room ID** and a **passphrase**. Both sides
independently derive the same key from those two values, so a passive observer
(or the relay process itself) cannot read the payload.

## How it works

```
            ┌──────────────────────┐                     ┌──────────────────────┐
            │   Client A (create)  │                     │   Client B (join)    │
            │   sender + server    │                     │   receiver + client  │
            ├──────────────────────┤   WebSocket (ws)    ├──────────────────────┤
  file ───▶ │ encrypt (AES-256-GCM)│ ── SYNC (cipher) ─▶ │ decrypt + write file │
            │ watch file for edits │                     │ send COMPLETE        │
            │ tear down on COMPLETE│ ◀─ COMPLETE ──────── │                      │
            └──────────────────────┘                     └──────────────────────┘
```

1. **Key derivation** — both peers run `scrypt(passphrase, salt = sha256("deaddrop_v1:" + roomID))`
   to produce a 256-bit AES key (`gateway/crypto.go`).
2. **Client A** (chose *create*) starts a WebSocket server, encrypts the source
   file, and pushes a `SYNC` frame to the peer. It also watches the file and
   re-sends if it changes locally before the transfer completes.
3. **Client B** (chose *join*) dials the server, receives the `SYNC` frame,
   authenticates and decrypts it with GCM, writes the plaintext to disk, and
   replies with `COMPLETE`.
4. On `COMPLETE`, the room is torn down.

## Project layout

| Path                  | Responsibility                                              |
| --------------------- | ----------------------------------------------------------- |
| `closeout.go`         | `main`, WebSocket server/client, read/write pumps, transfer |
| `client.go`           | `Client` type and connection constants                      |
| `prompt.go`           | Interactive CLI prompts (action, room, file, passphrase)    |
| `room.go`             | Room lifecycle, idle/transfer timers, teardown              |
| `roommanager.go`      | Tracks active rooms                                         |
| `watcher.go`          | Polling file watcher with network-write suppression         |
| `tunnel.go`           | Auto-managed Cloudflare quick tunnel (download, verify, run) |
| `gateway/crypto.go`   | scrypt key derivation                                       |
| `gateway/encrypt.go`  | AES-256-GCM encryption                                       |
| `gateway/decrypt.go`  | AES-256-GCM decryption / authentication                     |
| `gateway/message.go`  | JSON wire envelope (`join` / `sync` / `complete`)           |

## Requirements

- [Go 1.25+](https://go.dev/dl/) to build from source, **or**
- [Docker](https://docs.docker.com/get-docker/) to run in a container.

## Build & run locally

```bash
go build -o deaddrop .
```

Open two terminals.

**Sender (Client A):**

```bash
./deaddrop
# 1. choose "2" (create a new room)
# 2. enter a room ID, e.g. "room1"
# 3. enter the path to the file you want to send
# 4. enter a passphrase
```

**Receiver (Client B):**

```bash
./deaddrop
# 1. choose "1" (join an existing room)
# 2. enter the same room ID
# 3. enter the same passphrase
# → the decrypted file is written to ./received_file
```

## Configuration

| Variable        | Used by    | Default                   | Description                          |
| --------------- | ---------- | ------------------------- | ------------------------------------ |
| `DEADDROP_ADDR` | sender (A) | `:8080`                   | Address the server listens on        |
| `DEADDROP_URL`  | receiver (B) | `ws://localhost:8080/ws` | WebSocket URL the receiver dials     |

To transfer between machines, set `DEADDROP_ADDR` on the sender (e.g.
`0.0.0.0:8080`) and point the receiver at it with
`DEADDROP_URL=ws://<sender-host>:8080/ws`.

## Trying it over the internet (free, no account needed)

DeadDrop connects peer-to-peer, so if your tester isn't on the same LAN, the
sender's port needs a public address. DeadDrop can set this up for you
automatically using [Cloudflare's quick tunnels](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/do-more-with-tunnels/trycloudflare/) —
free, no account, no DNS setup, and no separate install step:

1. **Sender (Client A):** run `./deaddrop`, choose "create", and when
   prompted `Make this room reachable over the internet via a free
   Cloudflare tunnel?`, answer `y`.
2. On first use, DeadDrop downloads a pinned, checksum-verified `cloudflared`
   build for your OS/arch into your user cache directory (reused on every
   later run — nothing is installed system-wide). It then launches the
   tunnel itself and prints a ready-to-share line:
   ```
   Public URL ready — share this with your peer:
     DEADDROP_URL=wss://<random-subdomain>.trycloudflare.com/ws
   ```
3. **Receiver (Client B):** paste that line before running DeadDrop:
   ```bash
   DEADDROP_URL=wss://<random-subdomain>.trycloudflare.com/ws ./deaddrop
   ```

If you'd rather manage the tunnel yourself (e.g. `cloudflared` is already on
your `PATH`), DeadDrop uses that copy instead of downloading its own.

Quick tunnels are ephemeral — the URL dies with the DeadDrop process (Ctrl+C
tears the tunnel down along with it) — and meant for exactly this kind of
one-off trial, not anything long-lived.

## Run with Docker

Build the image:

```bash
docker build -t deaddrop:latest .
```

Because it is an interactive CLI, run each role as a one-off container with a
TTY so the passphrase prompt stays masked:

```bash
mkdir -p outbox inbox && cp mysecret.txt outbox/

# Terminal 1 — sender
docker compose run --rm --service-ports sender
#   → 2, room1, /data/mysecret.txt, passphrase

# Terminal 2 — receiver
docker compose run --rm receiver
#   → 1, room1, passphrase
#   → decrypted file appears in ./inbox/received_file
```

Or with plain `docker run` on a shared network:

```bash
docker network create deaddrop-net
docker run -it --rm --name sender --network deaddrop-net \
  -v "$PWD/outbox:/data:ro" deaddrop:latest
docker run -it --rm --network deaddrop-net \
  -e DEADDROP_URL=ws://sender:8080/ws \
  -v "$PWD/inbox:/home/deaddrop" deaddrop:latest
```

## Security notes

- Payloads are encrypted with **AES-256-GCM**; GCM authenticates the ciphertext,
  so a wrong passphrase or any tampering fails to decrypt without writing
  anything to disk.
- The AES key is derived locally with **scrypt** and never leaves either peer.
- Choose a strong passphrase and share the room ID / passphrase over a trusted
  channel. The security of the transfer rests entirely on the passphrase.
- `CheckOrigin` currently allows all WebSocket origins. Combined with the
  public tunnel option, anyone who guesses the room ID before your peer joins
  can occupy that single connection slot — they'd only ever see ciphertext
  they can't decrypt without the passphrase, but it would deny service to your
  actual peer. Use a hard-to-guess room ID (not "room1") whenever the tunnel
  is enabled.
- The auto-downloaded `cloudflared` binary (see "Trying it over the internet")
  is fetched over HTTPS from GitHub and verified against a sha256 pinned in
  `tunnel.go` before it's ever executed; it's never trusted on download
  alone.

## Known limitations

- The transfer is **one-shot**: after a successful `COMPLETE` the room is torn
  down. The sender's server process keeps running and must be stopped manually.
- Single sender ↔ single receiver per room; no relay/fan-out to multiple peers.
- Maximum file size is bounded by the WebSocket read limit (32 MiB).
