import { showHUD } from "@raycast/api";
import { errorText, runTailpaste } from "./tailpaste.ts";

export default async function command() {
  try {
    await showHUD(`📋 ${await runTailpaste(["push"])}`);
  } catch (error) {
    await showHUD(`⚠️ ${errorText(error)}`);
  }
}
