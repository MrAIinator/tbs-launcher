import fs from "node:fs";
import path from "node:path";
import { downloadFile, sha1File } from "./download.js";
import { GAME_DIR, safeJoin } from "./paths.js";

async function pool(items, limit, worker) {
  const queue = [...items];
  if (queue.length === 0) return;
  const runners = Array.from({ length: Math.min(limit, queue.length) }, async () => {
    while (queue.length) {
      const item = queue.shift();
      await worker(item);
    }
  });
  await Promise.all(runners);
}

function sha1Buffer(buffer) {
  return import("node:crypto").then((crypto) => crypto.createHash("sha1").update(buffer).digest("hex"));
}

export async function installMrpack(mrpackPath, hooks) {
  const { onLog, onProgress, isCancelled, previous } = hooks;
  const { default: AdmZip } = await import("adm-zip");
  const zip = new AdmZip(mrpackPath);
  const indexEntry = zip.getEntry("modrinth.index.json");
  if (!indexEntry) throw new Error("в mrpack нет modrinth.index.json");
  const index = JSON.parse(indexEntry.getData().toString("utf8"));
  const minecraft = index.dependencies?.minecraft;
  const neoforge = index.dependencies?.neoforge;
  if (!minecraft || !neoforge) throw new Error("в сборке не указаны версии игры");
  const files = (index.files || []).filter((file) => (file.env?.client || "required") !== "unsupported");
  const installed = {};
  let done = 0;

  await pool(files, 3, async (file) => {
    if (isCancelled?.()) throw new Error("отменено");
    const dest = safeJoin(GAME_DIR, file.path);
    const sha1 = file.hashes?.sha1;
    if (sha1 && fs.existsSync(dest) && (await sha1File(dest)) === sha1) {
      installed[file.path] = sha1;
    } else {
      const url = file.downloads?.[0];
      if (!url) throw new Error(`нет ссылки для ${file.path}`);
      onLog?.(`качаю ${path.basename(file.path)}`);
      await downloadFile(url, dest, {
        sha1,
        isCancelled,
        onProgress: ({ received, total }) =>
          onProgress?.({ phase: "моды", current: done, total: files.length, label: path.basename(file.path), received, fileTotal: total }),
      });
      installed[file.path] = sha1 || (await sha1File(dest));
    }
    done += 1;
    onProgress?.({ phase: "моды", current: done, total: files.length, label: path.basename(file.path) });
  });

  for (const entry of zip.getEntries()) {
    if (entry.isDirectory) continue;
    if (!entry.entryName.startsWith("overrides/")) continue;
    const rel = entry.entryName.slice("overrides/".length);
    if (!rel) continue;
    const dest = safeJoin(GAME_DIR, rel);
    const data = entry.getData();
    fs.mkdirSync(path.dirname(dest), { recursive: true });
    fs.writeFileSync(dest, data);
    installed[`override:${rel}`] = await sha1Buffer(data);
  }

  if (previous?.files) {
    for (const [rel, oldHash] of Object.entries(previous.files)) {
      if (installed[rel]) continue;
      const diskRel = rel.startsWith("override:") ? rel.slice("override:".length) : rel;
      let dest;
      try {
        dest = safeJoin(GAME_DIR, diskRel);
      } catch {
        continue;
      }
      if (!fs.existsSync(dest)) continue;
      if ((await sha1File(dest)) === oldHash) fs.rmSync(dest, { force: true });
    }
  }

  onLog?.(`сборка ${index.versionId || ""} синхронизирована`);
  return {
    packVersion: index.versionId || "0",
    minecraft,
    neoforge,
    name: index.name || "",
    files: installed,
  };
}
