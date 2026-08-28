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

const defaultBinary = "/usr/local/bin/tailpaste";

// Raycast fills in the manifest default, but hands back an empty string if the
// user clears the field, so fall back here too rather than exec'ing "".
function binary(): string {
  return getPreferenceValues<Preferences>().binaryPath?.trim() || defaultBinary;
}

/**
 * Raycast spawns commands with the GUI launch environment, not your shell's, so
 * PATH can be almost empty. The binary looks up pbpaste/pbcopy by name, so make
 * sure the directory they live in is on the PATH it inherits. Duplicate entries
 * are harmless.
 */
function childEnv(): NodeJS.ProcessEnv {
  return {
    ...process.env,
    PATH: [process.env.PATH, "/usr/bin:/bin"].filter(Boolean).join(":"),
  };
}

/**
 * Shells out to the tailpaste binary rather than reimplementing the HTTP call
 * here, so the token never leaves the binary.
 */
export async function runTailpaste(args: string[]): Promise<string> {
  const { stdout } = await execFileAsync(binary(), args, { timeout: 10_000, env: childEnv() });
  return stdout.trim();
}

/** Turns an execFile rejection into the binary's own error message. */
export function errorText(error: unknown): string {
  if (typeof error === "object" && error !== null) {
    const stderr = (error as { stderr?: string }).stderr;
    if (stderr?.trim()) {
      const lines = stderr.split("\n").map((line) => line.trim());
      const meaningful = lines.filter(Boolean);
      // The binary can print setup notices before the fatal error — creating a
      // fresh config file, for one — so prefer its own "tailpaste: ..." line
      // over whatever happens to come first.
      const fatal = meaningful.findLast((line) => line.startsWith("tailpaste:"));
      // meaningful is non-empty here: stderr.trim() was truthy, so at least one line has content.
      return (fatal ?? meaningful[meaningful.length - 1]!).replace(/^tailpaste:\s*/, "");
    }
    const message = (error as { message?: string }).message;
    if (message) return message;
  }
  return String(error);
}

/**
 * Read straight from the config file: the binary has no command that prints the
 * configured peers on their own (`tailpaste peers` lists the whole tailnet, not
 * this list). TAILPASTE_CONFIG is honoured to match the binary's own lookup.
 */
export function configuredPeers(): string[] {
  const path = process.env.TAILPASTE_CONFIG || join(homedir(), ".config", "tailpaste", "config.json");
  const config = JSON.parse(readFileSync(path, "utf8")) as Config;
  return config.peers ?? [];
}
