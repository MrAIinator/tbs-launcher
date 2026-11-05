import fs from "node:fs";
import path from "node:path";
import { downloadFile } from "./download.js";
import { CACHE_DIR, DATA_DIR, LAUNCHER_DIR } from "./paths.js";

export function compareVer(a, b) {
  const pa = String(a).split(/[.+]/).map((part) => parseInt(part, 10) || 0);
  const pb = String(b).split(/[.+]/).map((part) => parseInt(part, 10) || 0);
  const length = Math.max(pa.length, pb.length);
  for (let i = 0; i < length; i += 1) {
    const delta = (pa[i] || 0) - (pb[i] || 0);
    if (delta) return delta < 0 ? -1 : 1;
  }
  return 0;
}

export function readBundledManifest() {
  return JSON.parse(fs.readFileSync(path.join(LAUNCHER_DIR, "manifest.json"), "utf8"));
}

export function readInstalled() {
  try {
    return JSON.parse(fs.readFileSync(path.join(DATA_DIR, "installed.json"), "utf8"));
  } catch {
    return null;
  }
}

export function writeInstalled(data) {
  fs.mkdirSync(DATA_DIR, { recursive: true });
  fs.writeFileSync(path.join(DATA_DIR, "installed.json"), JSON.stringify(data));
}

export async function resolveManifest() {
  const bundled = readBundledManifest();
  if (!bundled.manifestUrl) return bundled;
  try {
    const response = await fetch(bundled.manifestUrl, {
      signal: AbortSignal.timeout(8000),
      headers: { "User-Agent": "TBSLauncher/1.0" },
    });
    if (!response.ok) return bundled;
    const remote = await response.json();
    if (!remote || typeof remote !== "object") return bundled;
    return { ...bundled, ...remote, bundledPackVersion: bundled.packVersion, bundledLauncherVersion: bundled.launcherVersion };
  } catch {
    return { ...bundled, bundledPackVersion: bundled.packVersion, bundledLauncherVersion: bundled.launcherVersion };
  }
}

export async function mrpackPathFor(manifest, hooks) {
  const bundledPath = path.join(LAUNCHER_DIR, "pack", "pack.mrpack");
  const bundledVersion = manifest.bundledPackVersion || manifest.packVersion;
  if (compareVer(bundledVersion, manifest.packVersion) >= 0 || !manifest.packUrl) return bundledPath;
  const dest = path.join(CACHE_DIR, `pack-${manifest.packVersion}.mrpack`);
  if (!fs.existsSync(dest)) {
    hooks.onLog?.(`качаю сборку ${manifest.packVersion}`);
    await downloadFile(manifest.packUrl, dest, {
      isCancelled: hooks.isCancelled,
      onProgress: ({ received, total }) => hooks.onProgress?.({ phase: "сборка", current: received, total, label: "mrpack" }),
    });
  }
  return dest;
}

export async function queueLauncherUpdate(manifest, hooks) {
  const local = manifest.bundledLauncherVersion || manifest.launcherVersion;
  if (!manifest.launcherUrl || compareVer(local, manifest.launcherVersion) >= 0) return false;
  const zipPath = path.join(CACHE_DIR, `launcher-${manifest.launcherVersion}.zip`);
  hooks.onLog?.(`качаю лаунчер ${manifest.launcherVersion}, встанет после перезапуска`);
  await downloadFile(manifest.launcherUrl, zipPath, { isCancelled: hooks.isCancelled });
  const { default: AdmZip } = await import("adm-zip");
  const pending = path.join(LAUNCHER_DIR, "updates", "pending");
  fs.rmSync(pending, { recursive: true, force: true });
  fs.mkdirSync(pending, { recursive: true });
  new AdmZip(zipPath).extractAllTo(pending, true);
  return true;
}
