<img src="raycast/assets/icon.png" alt="tailpaste" width="96">

# tailpaste

Copy on one Mac. Paste on the other. **tailpaste** sends clipboard text between your Macs and your iPhone over [Tailscale](https://tailscale.com).

Apple's Universal Clipboard works only when both devices are on the same Wi-Fi and in Bluetooth range. tailpaste needs only the same tailnet. A different network, a different city, or cellular all work.

## Raycast

The Raycast extension gives you three commands. Give each one a hotkey in Raycast settings.

| Command | What it does | Suggested hotkey |
| --- | --- | --- |
| **Push Clipboard** | Sends this Mac's clipboard to your other Mac. | `⌥C` |
| **Pull Clipboard** | Copies your other Mac's clipboard into this Mac's clipboard. | `⌥V` |
| **Push Clipboard to…** | Shows your peers. Pick one, and it gets the clipboard. | |

To install the extension, run this in the `raycast` folder. You need Node 22.22.2 or later.

```bash
npm install && npm run dev
```

After Raycast shows the commands, you can stop `npm run dev`. The commands stay in Raycast.

If the binary is not at `/usr/local/bin/tailpaste`, set its path in the extension preferences.

## iPhone

A Shortcut in the share sheet does the job. You do not need an app.

**Send text from the iPhone to your Macs:**

1. Make a new shortcut and turn on **Show in Share Sheet**.
2. Add the action **Get Contents of URL**, with these settings:
   - **URL:** `http://tailpaste-mac-a.<tailnet>.ts.net:8787/clip?fanout=1`
   - **Method:** `POST`
   - **Headers:** `Authorization` = `Bearer <your token>`
   - **Request Body:** Text = `Shortcut Input`
3. Share text to the shortcut from any app.

Mac A gets the text, then sends it to your other Macs. If Mac B is asleep, Mac A still gets the text.

**Get text from a Mac on the iPhone:** use the same URL with the `GET` method. Then add a **Copy to Clipboard** action.

If your action can send only JSON, use `/iosclip?fanout=1` instead. Set the body to a **Dictionary** with one key: `content` = `Shortcut Input`.

## Install

You need macOS, Go, and Tailscale on each Mac.

**On each Mac:**

1. Clone the repo and run the installer.

   ```bash
   git clone https://github.com/aabedraba/tailpaste && cd tailpaste
   ./install/install.sh
   ```

2. The installer prints a login URL. Open it, and approve the node on the tailnet that your other devices use.
3. If macOS asks to accept incoming network connections, click **Allow**.

**Then connect the Macs:**

1. Run `tailpaste init` on each Mac. It shows the token and the node name, for example `tailpaste-mac-a`.
2. To get the full name of each node, run `tailpaste peers`. A full name looks like `tailpaste-mac-a.<tailnet>.ts.net`.
3. Open `~/.config/tailpaste/config.json` on each Mac, and change two fields. Keep the other fields.

   ```json
   {
     "token": "3f9a1c04e8b27d6a05fe1188ab34c290",
     "peers": ["tailpaste-mac-b.<tailnet>.ts.net"]
   }
   ```

   Both Macs must use the **same token**. Use Mac A's token on Mac B. In `peers`, put the full name of the other Mac.

4. Restart the daemon on each Mac.

   ```bash
   launchctl kickstart -k gui/$(id -u)/com.abdallah.tailpaste
   ```

5. Do a test. On Mac A, run `echo "hello" | pbcopy && tailpaste push`. On Mac B, run `pbpaste`. You see `hello`.

## CLI

```bash
tailpaste push            # send this clipboard to the first peer
tailpaste pull            # get the clipboard of the first peer
tailpaste push mac-b      # send to one specific peer
tailpaste push --fanout   # send, and the peer sends it on to all other peers
tailpaste init            # show the config path, the token, and the peers
tailpaste peers           # list the machines on your tailnet
```

## Features

- **Any network.** If both devices are on your tailnet, tailpaste works. They do not need the same Wi-Fi.
- **Only on a keypress.** tailpaste never reads the clipboard by itself. A password that you copy from 1Password stays on that Mac.
- **Its own tailnet node.** The daemon joins your tailnet as a separate node. If you switch the Tailscale app to a work profile, tailpaste continues to work.
- **Two locks on each request.** The sender must have a tailnet address and the shared token.
- **Small.** It is a few hundred lines of Go. The only dependency is Tailscale's own `tsnet`.

## Not included

- **Clipboard history.** `pbpaste` is the source of truth. There is no database.
- **Images and files.** Use [Taildrop](https://tailscale.com/kb/1106/taildrop) for these.

## More

The [reference](docs/reference.md) has the HTTP API, the security model, the tailnet profiles, and the debugging steps.
