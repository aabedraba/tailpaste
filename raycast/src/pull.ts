import { showHUD } from "@raycast/api";
import { errorText, runTailpaste } from "./tailpaste";

export default async function command() {
  try {
    await showHUD(`📋 ${await runTailpaste(["pull"])}`);
  } catch (error) {
    await showHUD(`⚠️ ${errorText(error)}`);
  }
}
