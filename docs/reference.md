# tailpaste reference

The [README](../README.md) has the setup and the daily use. This page has the details.

## How it works

```
   ⌥C on Mac A ──▶  tailpaste push
                      pbpaste ──▶ POST /clip ──▶ Mac B daemon ──▶ pbcopy

   ⌥V on Mac A ──▶  tailpaste pull
                      GET /clip ◀── Mac B daemon      ──▶ pbcopy

   iPhone share sheet ──▶ POST /clip?fanout=1 ──▶ Mac A ──▶ pbcopy
                                                    └──▶ POST /clip ──▶ Mac B
```

One binary has two roles:

- `tailpaste daemon` is the only part that runs all the time. It receives clips, and it holds the tailnet node of this Mac. launchd starts it at login.
- `tailpaste push` and `tailpaste pull` run once and stop. They ask the local daemon to send the clip, so the clip goes out over the daemon's tailnet node. If no daemon runs, they connect to the peer directly.

### Why nothing polls the clipboard

Clips move only when you press a key. There are two reasons:

- macOS 15.4 added `NSPasteboard.accessBehavior`. macOS 26 alerts the user when an app reads the clipboard without a paste action. A loop that polls `changeCount` is the pattern that these alerts target.
- A password that you copy from 1Password cannot go to another machine. tailpaste does not need to filter [`org.nspasteboard.ConcealedType`](https://nspasteboard.org/) for this.

## HTTP API

Request and response bodies are raw `text/plain`, not JSON. The one exception is `/iosclip`, because some iOS Shortcuts actions can send only a JSON body.

| Method | Path | Behavior |
| --- | --- | --- |
| `POST` | `/clip` | The body is the text. Sets the clipboard of this machine. |
| `POST` | `/clip?fanout=1` | Sets the clipboard, then sends the text one time to each configured peer. |
| `GET` | `/clip` | Returns the clipboard of this machine as `text/plain`. |
| `POST` | `/iosclip` | The body is `{"content": "<text>"}`. Otherwise the same as `POST /clip`, `fanout=1` included. |
| `POST` | `/relay?peer=X` | Sends the body to one peer. The clipboard of this machine does not change. |
| `GET` | `/fetch?peer=X` | Returns the clipboard of one peer. |
| `GET` | `/health` | Returns `ok <name>`. This is the only route that does not need the token. |

### `/relay` and `/fetch`

These two routes are for the CLI on the same machine. A tsnet state directory has a single writer. The daemon owns the node, so a short `tailpaste push` cannot use it. The CLI asks the daemon to send the clip instead. This keeps `push`, `pull`, and the Raycast commands on the daemon's tailnet node.

Both routes take a peer name from the request. Both refuse a name that is **not in `peers`**. Without this check, a person with the token could use the daemon as an open proxy. `TestRequestedPeerRejectsUnconfigured` tests this. If you push to a peer that is not in the config, `push` connects to it directly and does not use the daemon.

### `/iosclip`

The body must have the `content` key. If the key is missing, the daemon returns `400`, because the shortcut is set up incorrectly. `{"content": ""}` clears the clipboard.

### The fanout loop guard

When a daemon sends a clip on to its peers, it sends it **without** `fanout`. Thus a relayed clip cannot start a second relay. There are no message IDs, no TTL, and no dedup cache. `TestRelayDoesNotReFanout` tests this.

If a relay fails, the response body says so, but the status is still `200`. The clip got to at least one machine.

## Security

Each route except `/health` has two checks:

1. The source address must be in the Tailscale ranges (`100.64.0.0/10` or `fd7a:115c:a1e0::/48`), or on loopback.
2. The request must have `Authorization: Bearer <token>`. The daemon compares the token in constant time.

The daemon serves the same routes on two listeners: its own tsnet node, and a plain socket on `0.0.0.0`. The plain socket binds to all addresses, not to the `100.x` address. Thus it starts before Tailscale is up, and it continues to work if the tailnet address changes. Check 1 controls who gets access. The plain listener also carries the loopback health check and the `/relay` calls from the CLI.

Copy the **token** to the other Mac. Never copy the `tsnet` state directory. Each node has its own identity and keys. If two machines share one state directory, they fight for the same node.

## Tailnet profiles

The Tailscale app can hold more than one profile, for example a personal tailnet and a work tailnet. But the app is logged in to **one profile at a time**. A switch takes the machine off the other tailnet.

For this reason the daemon runs its own tailnet node with [`tsnet`](https://tailscale.com/kb/1244/tsnet). The node has its own identity and keys in `~/.config/tailpaste/tsnet`.

```
   Tailscale.app    ─▶  work tailnet       ← switch this freely
   tailpaste daemon ─▶  personal tailnet   ← stays put
```

The results:

- **A profile switch has no effect.** You can get to the daemon through either profile, from any network, cellular included.
- **Work-tailnet ACLs have no effect.** The daemon does not use the work tailnet. A default-deny ACL on it cannot block tailpaste.

`tsnet` does its networking in user space. The node does not use a TUN device or root access, and it does not compete with the Tailscale app for port 8787.

In the admin console, the daemon's node shows as `linux`, not `macOS`. The label is cosmetic.

The node name is not the name of the Mac. Each Mac has two nodes: the Mac itself and the daemon. In `peers`, use the daemon's node name, for example `tailpaste-mac-a.<tailnet>.ts.net`.

### Check which node serves

```bash
$ tailpaste init
tsnet   tailpaste-mac-a (authenticated)

$ grep 'tailnet node' ~/Library/Logs/tailpaste.log
tailnet node up as tailpaste-mac-a.tailnet-name.ts.net:8787
```

### Log in again, or move to a different tailnet

The daemon holds the state directory, so stop it first.

```bash
launchctl bootout gui/$(id -u)/com.abdallah.tailpaste
rm -rf ~/.config/tailpaste/tsnet          # only if you change the tailnet
tailpaste login
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.abdallah.tailpaste.plist
```

`tailpaste login [name]` takes an optional node name. The default is `tailpaste-<machine name>`. If two Macs have the same machine name, give each one a different name. Otherwise Tailscale adds a number to the second name.

To skip the browser login, set `TS_AUTHKEY` to a [reusable auth key](https://tailscale.com/kb/1085/auth-keys).

### Turn tsnet off

Set `tsnet.enabled` to `false` in the config. The daemon then uses the connection of the Tailscale app, and it stops when you switch profiles.

## HTTPS for the iPhone

If Shortcuts refuses plain `http://`, put a real certificate in front of the daemon. You do not need to change the code.

```bash
tailscale serve --bg 8787      # → https://mac-a.tailnet-name.ts.net
```

Use that HTTPS URL in the shortcut. This URL goes to the node of the **Mac**, not the node of the daemon. It works only while the Tailscale app is on that tailnet, so you lose the profile independence.

## Config

`~/.config/tailpaste/config.json`:

```json
{
  "port": 8787,
  "token": "3f9a1c04e8b27d6a05fe1188ab34c290",
  "peers": ["tailpaste-mac-b.tailnet-name.ts.net"],
  "max_bytes": 1048576,
  "tsnet": { "enabled": true, "hostname": "tailpaste-mac-a" }
}
```

The daemon reads the config when it starts. After a change, restart it:

```bash
launchctl kickstart -k gui/$(id -u)/com.abdallah.tailpaste
```

To move the config to the other Mac, you can use Taildrop:

```bash
tailscale file cp ~/.config/tailpaste/config.json mac-b:   # on Mac A
mv ~/Downloads/config.json ~/.config/tailpaste/config.json # on Mac B
```

Then change `peers` and `tsnet.hostname` on Mac B.

## Raycast extension

The extension runs the `tailpaste` binary. It does not make the HTTP call itself, so the token stays in the binary. **Push Clipboard to…** reads `config.json` directly to get the peer list, because no subcommand prints only that list.

Before you publish the extension, run `npm run lint` and `npm run build`.

## Debugging

The daemon logs each request:

```bash
tail -f ~/Library/Logs/tailpaste.log
```

```
POST /clip?fanout=1 from 100.101.102.103 -> 200
set clipboard (31 bytes) from 100.101.102.103:52341
POST /clip from 100.64.0.9 -> 200          ← the relayed hop, note: no fanout
denied GET /clip from 192.168.1.7:9001: source is not on the tailnet
```

To check if a peer is reachable, without a change to the clipboard:

```bash
curl http://tailpaste-mac-b.tailnet-name.ts.net:8787/health     # ⇒ ok mac-b
```

To see the daemon live, run it in the foreground. First stop the launchd agent, because the agent holds port 8787 and the tsnet state directory.

```bash
launchctl bootout gui/$(id -u)/com.abdallah.tailpaste
tailpaste daemon
```

You can run two daemons on one machine. The test suite does this to simulate two Macs. Give the second daemon its own port, and set `tsnet.enabled` to `false` in its config.

```bash
TAILPASTE_CONFIG=/tmp/a.json tailpaste daemon
```

## Tests

```bash
go test ./...
```

The tests cover the address check, the token check (with the case where the token is a prefix of the header), peer URL normalization, the fanout loop guard, node-name derivation, and the refusal of an unconfigured peer on `/relay` and `/fetch`.

## Not included, and why

- **Clipboard history.** `pbpaste` is the source of truth. There is no database.
- **Auto-sync on change.** See [Why nothing polls the clipboard](#why-nothing-polls-the-clipboard). If you want it later, add an `auto_push` config flag that uses `POST /clip`. Nothing in the code prevents this.
- **Images and files.** Taildrop does these well. tailpaste covers the gap that Taildrop leaves, which is text.

`tsnet` was on this list before. The token and the address check gave the same security in about 15 lines, without a large dependency. That stopped being true when the daemon had to work across a switch between two tailnets. Nothing that uses the connection of the app can do that. The cost was real: 45 modules, a binary that grew from 10 MB to 29 MB, and one interactive login on each machine.
