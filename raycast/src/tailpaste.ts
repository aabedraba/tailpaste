import { execFile } from "child_process";
import { readFileSync } from "fs";
import { homedir } from "os";
import { join } from "path";
import { promisify } from "util";
import { getPreferenceValues } from "@raycast/api";

const execFileAsync = promisify(execFile);

interface Preferences {
  binaryPath?: string;
}

interface Config {
  peers?: string[];
}

function binary(): string {
  return getPreferenceValues<Preferences>().binaryPath || "/usr/local/bin/tailpaste";
}

/**
 * Shells out to the tailpaste binary rather than reimplementing the HTTP call
 * here, so the token and peer list live in exactly one place.
 */
export async function runTailpaste(args: string[]): Promise<string> {
  const { stdout } = await execFileAsync(binary(), args, { timeout: 10_000 });
  return stdout.trim();
}

/** Turns an execFile rejection into the binary's own stderr message. */
export function errorText(error: unknown): string {
  if (typeof error === "object" && error !== null) {
    const stderr = (error as { stderr?: string }).stderr;
    if (stderr && stderr.trim()) {
      return stderr.trim().replace(/^tailpaste:\s*/, "");
    }
    const message = (error as { message?: string }).message;
    if (message) return message;
  }
  return String(error);
}

export function configuredPeers(): string[] {
  const path = join(homedir(), ".config", "tailpaste", "config.json");
  const config = JSON.parse(readFileSync(path, "utf8")) as Config;
  return config.peers ?? [];
}
