# tailpaste

Share clipboard **text** between your Macs (and your iPhone) over Tailscale.

Apple's Universal Clipboard needs both devices on the *same Wi-Fi network*, within Bluetooth
range, on the same Apple Account. tailpaste only needs them on the same tailnet — different
networks, different bands, different cities all work identically.

Tailscale already does the hard parts: encryption, NAT traversal, stable addressing. This is
deliberately a thin pipe on top of it — about 250 lines of Go with no dependencies outside the
standard library.

## How it works

```
   ⌥C on Mac A ──▶  tailpaste push
                      pbpaste ──▶ POST /clip ──▶ Mac B daemon ──▶ pbcopy

   ⌥V on Mac A ──▶  tailpaste pull
                      GET /clip ◀── Mac B daemon      ──▶ pbcopy

   iPhone share sheet ──▶ POST /clip?fanout=1 ──▶ Mac A ──▶ pbcopy
                                                    └──▶ POST /clip ──▶ Mac B
```

One binary, two roles:

- `tailpaste daemon` — the only long-running part. Receives. Runs on each Mac under launchd.
- `tailpaste push` / `pull` — stateless one-shot commands. The *sending* side needs no daemon.

**Nothing polls the clipboard.** Clips move only when you press a key. That is not just a
simplicity choice:

- macOS 15.4 added `NSPasteboard.accessBehavior`, and macOS 26 alerts the user when an app reads
  the clipboard without a paste-related interaction. A `changeCount` polling loop is exactly the
  pattern that targets.
- It makes it impossible to leak a password copied from 1Password to another machine, without
  needing to filter [`org.nspasteboard.ConcealedType`](https://nspasteboard.org/).

## Install

On **each** Mac:

```bash
git clone https://github.com/aabedraba/tailpaste && cd tailpaste
./install/install.sh
```

That builds the binary to `/usr/local/bin/tailpaste`, writes
`~/.config/tailpaste/config.json` with a fresh random token, and loads the launchd agent.

> macOS will ask *"Do you want the application tailpaste to accept incoming network connections?"*
> on first launch. Click Allow — once.

Then point the two machines at each other. `tailpaste peers` lists your tailnet:

```bash
$ tailpaste peers
mac-b.tailnet-name.ts.net                macOS      online
iphone.tailnet-name.ts.net               iOS        online
```

Edit `~/.config/tailpaste/config.json` on Mac A:

```json
{
  "port": 8787,
  "token": "3f9a…",
  "peers": ["mac-b.tailnet-name.ts.net"],
  "max_bytes": 1048576
}
```

…and the mirror image on Mac B. **The token must be identical on both machines** — copy the file
across and just swap the `peers` entry. Then restart both daemons:

```bash
launchctl kickstart -k gui/$(id -u)/com.abdallah.tailpaste
```

## Use

```bash
tailpaste push            # send this clipboard to the first configured peer
tailpaste pull            # fetch that peer's clipboard into this one
tailpaste push mac-b      # target a specific peer
tailpaste push --fanout   # send, and have the peer relay onward to everyone else
```

Bind these to hotkeys via the Raycast extension in `raycast/` — see below.

## HTTP API

Small enough to drive entirely from `curl`, which is the point. Bodies are raw `text/plain`,
never JSON, so an iOS Shortcut can post one without a serialization step.

| Method | Path | Behaviour |
|---|---|---|
| `POST` | `/clip` | Body is the text. Sets this machine's clipboard. |
| `POST` | `/clip?fanout=1` | …and relays once to every configured peer. |
| `GET` | `/clip` | Returns this machine's clipboard as `text/plain`. |
| `GET` | `/health` | Liveness check. The only unauthenticated route. |

Two independent gates guard `/clip`:

1. The source address must be inside Tailscale's ranges (`100.64.0.0/10` or
   `fd7a:115c:a1e0::/48`), or loopback.
2. `Authorization: Bearer <token>`, compared in constant time.

The daemon binds `0.0.0.0` rather than the `100.x` address, so it survives starting before
Tailscale is up and doesn't break if the tailnet address changes. Gate 1 is what restricts access.

**`fanout` is the loop guard.** Relayed requests are sent *without* the parameter, so a relay can
never trigger another relay. Hop depth is capped structurally — no message IDs, no TTL, no dedup
cache. `TestRelayDoesNotReFanout` pins this down.

## iPhone

No app needed — a Shortcut on the share sheet is enough.

- **Action:** Get Contents of URL
- **URL:** `http://mac-a.tailnet-name.ts.net:8787/clip?fanout=1`
- **Method:** `POST`
- **Headers:** `Authorization` → `Bearer <your token>`
- **Request Body:** Text → `Shortcut Input`

`fanout=1` means the phone sends **one** request and both Macs get the text — so the shortcut
doesn't need two URL actions, and doesn't fail when one Mac is asleep. A relay failure is reported
in the response body but still returns `200`, because the clip *did* land somewhere.

For the other direction (Mac clipboard → phone), use `GET` on the same URL followed by a
**Copy to Clipboard** action.

If Shortcuts refuses plain `http://`, put a real certificate in front with no code changes:

```bash
tailscale serve --bg 8787      # → https://mac-a.tailnet-name.ts.net
```

Then use that HTTPS URL in the shortcut instead.

**Files and images are out of scope** — Taildrop already handles those well. tailpaste covers the
gap Taildrop leaves, which is text.

## Raycast

```bash
cd raycast && npm install && npm run dev
```

Three commands — Push Clipboard, Pull Clipboard, and Push Clipboard To… (pick a peer). Assign
hotkeys in Raycast's own settings. The extension shells out to the `tailpaste` binary rather than
reimplementing the HTTP call, so the token and peer list live in exactly one place.

## Debugging

Everything the daemon does is logged:

```bash
tail -f ~/Library/Logs/tailpaste.log
```

```
POST /clip?fanout=1 from 100.101.102.103 -> 200
set clipboard (31 bytes) from 100.101.102.103:52341
POST /clip from 100.64.0.9 -> 200          ← the relayed hop, note: no fanout
denied GET /clip from 192.168.1.7:9001: source is not on the tailnet
```

Check reachability without touching the clipboard:

```bash
curl http://mac-b.tailnet-name.ts.net:8787/health     # ⇒ ok mac-b
```

Run it in the foreground to watch it live:

```bash
launchctl bootout gui/$(id -u)/com.abdallah.tailpaste
tailpaste daemon
```

Run two daemons on one machine (this is how the test suite simulates two Macs):

```bash
TAILPASTE_CONFIG=/tmp/a.json tailpaste daemon
```

## Tests

```bash
go test ./...
```

Covers the address gate, the token check (including the "token is a prefix of the header" case),
peer URL normalisation, and the fanout loop guard.

## Deliberately not included

- **Clipboard history** — `pbpaste` is the source of truth. There is no database.
- **Auto-sync on change** — see the polling note above. If it's ever wanted, it's an `auto_push`
  config flag reusing `POST /clip`; nothing here blocks it.
- **tsnet** — embedding a Tailscale node would give free `whois` identity and automatic HTTPS, but
  costs a device per instance, an auth-key flow, and a large dependency. The token plus the
  address gate reach the same place in about 15 lines.
- **Images and files** — Taildrop.
