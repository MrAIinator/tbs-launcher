import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));

export const LAUNCHER_DIR = path.resolve(here, "..");
export const DATA_DIR = process.env.APPDATA
  ? path.join(process.env.APPDATA, "TBSLauncher")
  : path.join(os.homedir(), ".tbs-launcher");
export const GAME_DIR = path.join(DATA_DIR, "game");
export const CACHE_DIR = path.join(DATA_DIR, "cache");
export const RUNTIME_DIR = path.join(DATA_DIR, "runtime");

export function safeJoin(root, rel) {
  const normalized = String(rel).replace(/\\/g, "/");
  if (!normalized || normalized.startsWith("/") || normalized.includes("\0") || normalized.split("/").includes("..")) {
    throw new Error(`плохой путь: ${rel}`);
  }
  const dest = path.resolve(root, normalized);
  const base = path.resolve(root);
  if (dest !== base && !dest.startsWith(base + path.sep)) throw new Error(`плохой путь: ${rel}`);
  return dest;
}
