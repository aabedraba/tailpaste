import { showHUD } from "@raycast/api";
import { errorText, runTailpaste } from "./tailpaste.ts";

export default async function command() {
  try {
    await showHUD(`📋 ${await runTailpaste(["pull"])}`);
  } catch (error) {
    await showHUD(`⚠️ ${errorText(error)}`);
  }
}
