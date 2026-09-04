# tailpaste

Share clipboard **text** between your Macs (and your iPhone) over Tailscale.

Apple's Universal Clipboard needs both devices on the *same Wi-Fi network*, within Bluetooth
range, on the same Apple Account. tailpaste only needs them on the same tailnet — different
networks, different bands, different cities all work identically.

Tailscale already does the hard parts: encryption, NAT traversal, stable addressing. This is
deliberately a thin pipe on top of it — a few hundred lines of Go, whose only dependency is
Tailscale's own `tsnet`.

The daemon joins the tailnet as **a node of its own** rather than borrowing the connection the
Tailscale app holds. The app can only be logged into one tailnet at a time, so if you switch it
between a personal and a work profile, anything riding on it goes offline. A node of its own keeps
working through either profile, and is unaffected by the ACLs on whichever tailnet the app is
currently using. See [Tailnet profiles](#tailnet-profiles).

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

- `tailpaste daemon` — the only long-running part. Receives, and holds this machine's tailnet node.
  Runs on each Mac under launchd.
- `tailpaste push` / `pull` — one-shot commands. They ask the local daemon to do the sending, so
  the clip goes out over the daemon's tailnet node; with no daemon running they dial the peer
  directly instead.

**Nothing polls the clipboard.** Clips move only when you press a key. That is not just a
simplicity choice:

- macOS 15.4 added `NSPasteboard.accessBehavior`, and macOS 26 alerts the user when an app reads
  the clipboard without a paste-related interaction. A `changeCount` polling loop is exactly the
  pattern that targets.
- It makes it impossible to leak a password copied from 1Password to another machine, without
  needing to filter [`org.nspasteboard.ConcealedType`](https://nspasteboard.org/).

## Install

### 1. On Mac A — build and install

```bash
git clone https://github.com/aabedraba/tailpaste && cd tailpaste
./install/install.sh
```

This builds the binary to `/usr/local/bin/tailpaste`, creates
`~/.config/tailpaste/config.json` with a fresh random token, joins the tailnet as a node of its
own, and loads the launchd agent so the daemon starts at login.

Joining the tailnet is the one interactive step. The installer prints a URL:

```
==> joining the tailnet as a node of its own
joining the tailnet as "tailpaste-mac-a"
To start this tsnet server, restart with TS_AUTHKEY set, or go to: https://login.tailscale.com/a/…
```

Open it and approve the node **on the tailnet your other devices are on**. If you keep a personal
and a work tailnet, this is the personal one. To skip the browser, put a
[reusable auth key](https://tailscale.com/kb/1085/auth-keys) in `TS_AUTHKEY` instead.

> macOS asks *"Do you want the application tailpaste to accept incoming network connections?"* on
> first launch. Click **Allow** — once.

Note the token it prints:

```
$ tailpaste init
config  /Users/you/.config/tailpaste/config.json
token   3f9a1c04e8b27d6a05fe1188ab34c290
port    8787
peers   (none yet — add your other Mac)
```

### 2. On Mac B — same thing

```bash
git clone https://github.com/aabedraba/tailpaste && cd tailpaste
./install/install.sh
```

Mac B generates its **own** token, which will not match Mac A's. Fixing that is step 4.

### 3. Find the MagicDNS names

Each daemon has its own node, so the name you point a peer at is the **daemon's** name — the one
`tailpaste login` printed — not the machine's Tailscale name. Both exist, and they are different
nodes:

```bash
$ tailpaste init
...
tsnet   tailpaste-mac-a (authenticated)
```

That node is `tailpaste-mac-a.tailnet-name.ts.net`. `tailpaste peers` still lists the whole
tailnet, which is handy for reading off the suffix:

```bash
$ tailpaste peers
tailpaste-mac-b.tailnet-name.ts.net      linux      online
mac-b.tailnet-name.ts.net                macOS      online
iphone.tailnet-name.ts.net               iOS        online
```

These names are what you put in `peers`. They work from any network, and through any profile the
Tailscale app happens to be switched to.

> The daemon's node reports as `linux` rather than `macOS`: `tsnet` is a userspace
> implementation, not the Tailscale app. It is a cosmetic label in the admin console.

### 4. Point them at each other, with one shared token

Pick **one** token — Mac A's will do — and use it on both machines.

`~/.config/tailpaste/config.json` on **Mac A**:

```json
{
  "port": 8787,
  "token": "3f9a1c04e8b27d6a05fe1188ab34c290",
  "peers": ["tailpaste-mac-b.tailnet-name.ts.net"],
  "max_bytes": 1048576,
  "tsnet": { "enabled": true, "hostname": "tailpaste-mac-a" }
}
```

The same file on **Mac B**, with `peers` and `tsnet.hostname` swapped:

```json
{
  "port": 8787,
  "token": "3f9a1c04e8b27d6a05fe1188ab34c290",
  "peers": ["tailpaste-mac-a.tailnet-name.ts.net"],
  "max_bytes": 1048576,
  "tsnet": { "enabled": true, "hostname": "tailpaste-mac-b" }
}
```

Since you already have Tailscale, Taildrop is the easiest way to move the file:

```bash
# on Mac A
tailscale file cp ~/.config/tailpaste/config.json mac-b:
# on Mac B — it lands in ~/Downloads
mv ~/Downloads/config.json ~/.config/tailpaste/config.json
# then edit "peers" and "tsnet.hostname" to be Mac B's
```

> Copy the **token** across, never the `tsnet` state directory. Each machine's node has its own
> identity and keys; two machines sharing one state directory would fight over the same node.

### 5. Restart both daemons and check

The daemon reads its config at startup, so restart it on **both** machines:

```bash
launchctl kickstart -k gui/$(id -u)/com.abdallah.tailpaste
```

Confirm each machine can see the other:

```bash
# from Mac A
curl http://tailpaste-mac-b.tailnet-name.ts.net:8787/health     # ⇒ ok mac-b
# from Mac B
curl http://tailpaste-mac-a.tailnet-name.ts.net:8787/health     # ⇒ ok mac-a
```

Then switch the Tailscale app to your other profile and run the same two commands. They must still
work — that is the whole point of the daemon having a node of its own.

Then a real round trip:

```bash
# on Mac A
echo "hello from A" | pbcopy && tailpaste push
# on Mac B
pbpaste                                                # ⇒ hello from A
```

If that works, you're done.

## Use

```bash
tailpaste init            # show config path, token and peers
tailpaste push            # send this clipboard to the first configured peer
tailpaste pull            # fetch that peer's clipboard into this one
tailpaste push mac-b      # target a specific peer
tailpaste push --fanout   # send, and have the peer relay onward to everyone else
```

Bind these to hotkeys via the Raycast extension in `raycast/` — see below.

## HTTP API

Small enough to drive entirely from `curl`, which is the point. Bodies are raw `text/plain`,
never JSON — except on `/iosclip`, which exists only because some iOS Shortcuts actions can
send a JSON body and nothing else.

| Method | Path | Behaviour |
|---|---|---|
| `POST` | `/clip` | Body is the text. Sets this machine's clipboard. |
| `POST` | `/clip?fanout=1` | …and relays once to every configured peer. |
| `GET` | `/clip` | Returns this machine's clipboard as `text/plain`. |
| `POST` | `/iosclip` | Body is `{"content": "<text>"}`. Otherwise identical to `POST /clip`, `fanout=1` included. |
| `POST` | `/relay?peer=X` | Forward the body to one peer. This machine's clipboard is untouched. |
| `GET` | `/fetch?peer=X` | Return one peer's clipboard. |
| `GET` | `/health` | Liveness check. The only unauthenticated route. |

`/relay` and `/fetch` exist for this machine's own CLI. A tsnet state directory has a single
writer, so the daemon owns the node and a short-lived `tailpaste push` cannot dial through it —
it asks the daemon to send instead. That is what keeps `push`/`pull` (and the Raycast commands,
which shell out to them) working over the daemon's tailnet rather than the app's.

Both routes take a peer name off the request, so both refuse any name that is **not already in
`peers`** — otherwise holding the token would buy an open proxy to any address the daemon can
reach. `TestRequestedPeerRejectsUnconfigured` pins that down. For a peer that is not in the
config, `push` skips the daemon and dials directly, exactly as it did before.

`/iosclip` requires the `content` key to be present; a body without it is a `400`, since that
means a shortcut is wired up wrong rather than that you meant to clear the clipboard.
`{"content": ""}` does clear it.

Two independent gates guard every route but `/health`:

1. The source address must be inside Tailscale's ranges (`100.64.0.0/10` or
   `fd7a:115c:a1e0::/48`), or loopback.
2. `Authorization: Bearer <token>`, compared in constant time.

The daemon serves the same routes on two listeners: its own tsnet node, and a plain `0.0.0.0`
socket. Binding broadly rather than to the `100.x` address means it survives starting before
Tailscale is up and doesn't break if the tailnet address changes; gate 1 is what restricts access.
The plain listener is also what carries the loopback health check and the CLI's `/relay` calls.

**`fanout` is the loop guard.** Relayed requests are sent *without* the parameter, so a relay can
never trigger another relay. Hop depth is capped structurally — no message IDs, no TTL, no dedup
cache. `TestRelayDoesNotReFanout` pins this down.

## iPhone

No app needed — a Shortcut on the share sheet is enough.

- **Action:** Get Contents of URL
- **URL:** `http://tailpaste-mac-a.tailnet-name.ts.net:8787/clip?fanout=1`
- **Method:** `POST`
- **Headers:** `Authorization` → `Bearer <your token>`
- **Request Body:** Text → `Shortcut Input`

`fanout=1` means the phone sends **one** request and both Macs get the text — so the shortcut
doesn't need two URL actions, and doesn't fail when one Mac is asleep. A relay failure is reported
in the response body but still returns `200`, because the clip *did* land somewhere.

If the action you're using can only send JSON, point it at `/iosclip?fanout=1` instead and set
the body to a **Dictionary** with one key, `content` → `Shortcut Input`. Everything else — auth,
fanout, the response — is the same.

For the other direction (Mac clipboard → phone), use `GET` on the same URL followed by a
**Copy to Clipboard** action.

If Shortcuts refuses plain `http://`, put a real certificate in front with no code changes:

```bash
tailscale serve --bg 8787      # → https://mac-a.tailnet-name.ts.net
```

Then use that HTTPS URL in the shortcut instead. Note that this fronts the **machine's** Tailscale
node, not the daemon's, so it only answers while the Tailscale app is on that tailnet — it gives up
the profile independence the daemon's own node buys you.

**Files and images are out of scope** — Taildrop already handles those well. tailpaste covers the
gap Taildrop leaves, which is text.

## Raycast

Needs Node 22.22.2 or newer — that is `@raycast/api` 2.x's own floor, and `npm install` warns
rather than fails if you are below it.

```bash
cd raycast && npm install && npm run dev
```

`npm run dev` installs the extension into Raycast in development mode and keeps rebuilding. Leave
it running while you use it; stop it and the commands stay installed. `npm run lint` and
`npm run build` are what to run before publishing.

Three commands — Push Clipboard, Pull Clipboard, and Push Clipboard to… (pick a peer). Assign
hotkeys in Raycast's own settings. If the binary isn't at `/usr/local/bin/tailpaste`, point the
extension at it in Raycast's extension preferences.

The extension shells out to the `tailpaste` binary rather than reimplementing the HTTP call, so
the token never leaves the binary. It does read `config.json` directly for the peer list in
Push Clipboard to…, since no subcommand prints just that list.

## Tailnet profiles

The Tailscale app can hold several profiles — a personal tailnet and a work one, say — but it is
logged into only **one at a time**. Switching profiles takes the machine off the other tailnet
entirely, which used to take the daemon with it: peers on the personal tailnet became unreachable
the moment the app switched to work, and no amount of restarting the daemon helped.

The daemon therefore runs its own tailnet node via
[`tsnet`](https://tailscale.com/kb/1244/tsnet), with its own identity and its own keys in
`~/.config/tailpaste/tsnet`:

```
   Tailscale.app   ──▶  work tailnet       ← switch this freely
   tailpaste daemon ─▶  personal tailnet   ← stays put
```

Two consequences worth knowing:

- **Switching profiles no longer matters.** The daemon is reachable through either, from any
  network, including cellular.
- **Work-tailnet ACLs no longer matter.** The daemon never uses the work tailnet, so a
  default-deny ACL there cannot block it and nobody has to change anything on that side.

`tsnet` is userspace networking, so this node coexists with the Tailscale app rather than
competing with it: no TUN device, no root, no conflict over port 8787 (the plain listener and the
node's listener are on different network stacks).

Check which node is serving:

```bash
$ tailpaste init
tsnet   tailpaste-mac-a (authenticated)

$ grep 'tailnet node' ~/Library/Logs/tailpaste.log
tailnet node up as tailpaste-mac-a.tailnet-name.ts.net:8787
```

To re-authenticate or move the node to a different tailnet, stop the daemon first — it holds the
state directory — then log in again:

```bash
launchctl bootout gui/$(id -u)/com.abdallah.tailpaste
rm -rf ~/.config/tailpaste/tsnet          # only when changing tailnet
tailpaste login
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.abdallah.tailpaste.plist
```

`tailpaste login [name]` takes an optional node name. Pass one when two machines report the same
macOS name — the derived default is `tailpaste-<machine name>`, and Tailscale would otherwise
disambiguate them for you with a numeric suffix.

Setting `tsnet.enabled` to `false` in the config reverts to the old behaviour: the daemon rides on
the Tailscale app's connection and stops working when you switch profiles.

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
curl http://tailpaste-mac-b.tailnet-name.ts.net:8787/health     # ⇒ ok mac-b
```

Run it in the foreground to watch it live. The agent has to go first, because it holds both port
8787 and the tsnet state directory:

```bash
launchctl bootout gui/$(id -u)/com.abdallah.tailpaste
tailpaste daemon
```

Run two daemons on one machine (this is how the test suite simulates two Macs). Give the second
one its own port and turn `tsnet` off in its config — two nodes on one machine would otherwise
need separate `state_dir` and `hostname` values, which is more setup than a local test needs:

```bash
TAILPASTE_CONFIG=/tmp/a.json tailpaste daemon
```

## Tests

```bash
go test ./...
```

Covers the address gate, the token check (including the "token is a prefix of the header" case),
peer URL normalisation, the fanout loop guard, node-name derivation, and — for the two routes that
take a peer name off the request — that an unconfigured peer is refused.

## Deliberately not included

- **Clipboard history** — `pbpaste` is the source of truth. There is no database.
- **Auto-sync on change** — see the polling note above. If it's ever wanted, it's an `auto_push`
  config flag reusing `POST /clip`; nothing here blocks it.
- **Images and files** — Taildrop.

`tsnet` used to be on this list — the reasoning was that the token plus the address gate reach the
same place in about 15 lines, without a device per instance or a large dependency. That held right
up until the daemon had to survive switching the Tailscale app between two tailnets, which nothing
riding on the app's connection can do. The costs were all real and were paid: 45 modules, a binary
that grew from 10 MB to 29 MB, and one interactive login per machine.
