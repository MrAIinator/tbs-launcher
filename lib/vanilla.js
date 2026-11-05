import fs from "node:fs";
import path from "node:path";
import { downloadFile, sha1File } from "./download.js";
import { GAME_DIR, safeJoin } from "./paths.js";

const MANIFEST = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json";

function osName() {
  if (process.platform === "win32") return "windows";
  if (process.platform === "darwin") return "osx";
  return "linux";
}

function allowed(rules) {
  if (!rules || rules.length === 0) return true;
  const name = osName();
  let matched = false;
  let allow = false;
  for (const rule of rules) {
    const os = rule.os;
    if (os?.name && os.name !== name) continue;
    if (os?.arch && os.arch !== process.arch) continue;
    matched = true;
    allow = rule.action === "allow";
  }
  return matched ? allow : false;
}

async function pool(items, limit, worker) {
  const queue = [...items];
  const runners = Array.from({ length: Math.min(limit, queue.length) }, async () => {
    while (queue.length) {
      const item = queue.shift();
      await worker(item);
    }
  });
  await Promise.all(runners);
}

function artifactOf(lib) {
  if (lib.downloads?.artifact) return lib.downloads.artifact;
  return null;
}

function nativeArtifact(lib) {
  const natives = lib.natives;
  if (!natives) return null;
  const key = natives[osName()];
  if (!key) return null;
  const arch = process.arch === "x64" ? "64" : "32";
  const classifier = key.replace("${arch}", arch);
  return lib.downloads?.classifiers?.[classifier] || null;
}

async function fetchJson(url) {
  const response = await fetch(url, { headers: { "User-Agent": "TBSLauncher/1.0" } });
  if (!response.ok) throw new Error(`HTTP ${response.status} ${url}`);
  return response.json();
}

export async function ensureVanilla(versionId, hooks) {
  const { onLog, onProgress, isCancelled, force } = hooks;
  const markerPath = path.join(GAME_DIR, "tbs-vanilla.json");
  if (!force && fs.existsSync(markerPath)) {
    try {
      const marker = JSON.parse(fs.readFileSync(markerPath, "utf8"));
      const jar = path.join(GAME_DIR, "versions", versionId, `${versionId}.jar`);
      if (marker.version === versionId && fs.existsSync(jar)) {
        onLog?.(`Minecraft ${versionId} уже на месте`);
        return;
      }
    } catch {
      /* reinstall */
    }
  }

  onLog?.(`качаю Minecraft ${versionId}`);
  const manifest = await fetchJson(MANIFEST);
  const entry = manifest.versions.find((item) => item.id === versionId);
  if (!entry) throw new Error(`в манифесте Mojang нет ${versionId}`);
  const version = await fetchJson(entry.url);
  const versionDir = path.join(GAME_DIR, "versions", versionId);
  fs.mkdirSync(versionDir, { recursive: true });
  fs.writeFileSync(path.join(versionDir, `${versionId}.json`), JSON.stringify(version));

  const client = version.downloads?.client;
  if (!client?.url) throw new Error("у версии нет client.jar");
  await downloadFile(client.url, path.join(versionDir, `${versionId}.jar`), {
    sha1: client.sha1,
    isCancelled,
    onProgress: ({ received, total }) => onProgress?.({ phase: "игра", current: received, total, label: "client.jar" }),
  });

  const libs = [];
  for (const lib of version.libraries || []) {
    if (!allowed(lib.rules)) continue;
    const artifact = artifactOf(lib);
    if (artifact?.url && artifact.path) libs.push(artifact);
    const native = nativeArtifact(lib);
    if (native?.url && native.path) libs.push(native);
  }

  let done = 0;
  await pool(libs, 8, async (artifact) => {
    if (isCancelled?.()) throw new Error("отменено");
    const dest = safeJoin(path.join(GAME_DIR, "libraries"), artifact.path);
    await downloadFile(artifact.url, dest, { sha1: artifact.sha1, isCancelled });
    done += 1;
    if (done % 20 === 0 || done === libs.length) {
      onProgress?.({ phase: "библиотеки", current: done, total: libs.length, label: "libraries" });
    }
  });

  const indexMeta = version.assetIndex;
  if (!indexMeta?.url || !indexMeta.id) throw new Error("нет индекса ассетов");
  const indexPath = path.join(GAME_DIR, "assets", "indexes", `${indexMeta.id}.json`);
  await downloadFile(indexMeta.url, indexPath, { sha1: indexMeta.sha1, isCancelled });
  const index = JSON.parse(fs.readFileSync(indexPath, "utf8"));
  const objects = Object.entries(index.objects || {});
  let assetsDone = 0;
  await pool(objects, 12, async ([, obj]) => {
    if (isCancelled?.()) throw new Error("отменено");
    const hash = obj.hash;
    const rel = `${hash.slice(0, 2)}/${hash}`;
    const dest = safeJoin(path.join(GAME_DIR, "assets", "objects"), rel);
    if (fs.existsSync(dest) && fs.statSync(dest).size === obj.size) {
      assetsDone += 1;
      return;
    }
    const url = `https://resources.download.minecraft.net/${rel}`;
    await downloadFile(url, dest, { sha1: hash, isCancelled });
    assetsDone += 1;
    if (assetsDone % 100 === 0 || assetsDone === objects.length) {
      onProgress?.({ phase: "ассеты", current: assetsDone, total: objects.length, label: "assets" });
    }
  });

  const jarOk = await sha1File(path.join(versionDir, `${versionId}.jar`));
  if (client.sha1 && jarOk !== client.sha1) throw new Error("client.jar не сошёлся");
  fs.writeFileSync(markerPath, JSON.stringify({ version: versionId, assets: objects.length }));
  onLog?.(`Minecraft ${versionId} готов`);
}
