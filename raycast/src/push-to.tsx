import { Action, ActionPanel, Icon, List, closeMainWindow, showHUD } from "@raycast/api";
import { configuredPeers, errorText, runTailpaste } from "./tailpaste";

export default function Command() {
  let peers: string[] = [];
  let loadError = "";
  try {
    peers = configuredPeers();
  } catch (error) {
    loadError = errorText(error);
  }

  async function pushTo(peer: string) {
    await closeMainWindow();
    try {
      await showHUD(`📋 ${await runTailpaste(["push", peer])}`);
    } catch (error) {
      await showHUD(`⚠️ ${errorText(error)}`);
    }
  }

  return (
    <List>
      <List.EmptyView
        icon={Icon.Warning}
        title={loadError ? "Cannot read config" : "No peers configured"}
        description={loadError || "Add one to ~/.config/tailpaste/config.json"}
      />
      {peers.map((peer) => (
        <List.Item
          key={peer}
          icon={Icon.Clipboard}
          title={peer}
          actions={
            <ActionPanel>
              <Action title="Push Clipboard Here" icon={Icon.Upload} onAction={() => pushTo(peer)} />
            </ActionPanel>
          }
        />
      ))}
    </List>
  );
}
