import { Action, ActionPanel, Icon, List, closeMainWindow, showHUD } from "@raycast/api";
import { useMemo } from "react";
import { configuredPeers, errorText, runTailpaste } from "./tailpaste.ts";

export default function Command() {
  // useMemo, not a bare call: the body runs again on every render, and this
  // reads a file off disk.
  const { peers, loadError } = useMemo(() => {
    try {
      return { peers: configuredPeers(), loadError: "" };
    } catch (error) {
      return { peers: [] as string[], loadError: errorText(error) };
    }
  }, []);

  async function pushTo(peer: string) {
    await closeMainWindow();
    try {
      await showHUD(`📋 ${await runTailpaste(["push", peer])}`);
    } catch (error) {
      await showHUD(`⚠️ ${errorText(error)}`);
    }
  }

  if (peers.length === 0) {
    return (
      <List>
        <List.EmptyView
          icon={Icon.Warning}
          title={loadError ? "Cannot read config" : "No peers configured"}
          description={loadError || "Add one to ~/.config/tailpaste/config.json"}
        />
      </List>
    );
  }

  return (
    <List>
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
