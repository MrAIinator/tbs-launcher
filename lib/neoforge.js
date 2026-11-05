import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { downloadFile } from "./download.js";
import { CACHE_DIR, GAME_DIR, RUNTIME_DIR } from "./paths.js";

function findJava(dir, depth = 0) {
  if (!fs.existsSync(dir) || depth > 5) return null;
  const entries = fs.readdirSync(dir, { withFileTypes: true });
  for (const entry of entries) {
    if (!entry.isFile()) continue;
    if (entry.name === "java.exe" || entry.name === "java") return path.join(dir, entry.name);
  }
  for (const entry of entries) {
    if (!entry.isDirectory()) continue;
    const found = findJava(path.join(dir, entry.name), depth + 1);
    if (found) return found;
  }
  return null;
}

export async function ensureJava(hooks) {
  const existing = findJava(path.join(RUNTIME_DIR, "java"));
  if (existing) return existing;
  if (process.platform !== "win32") {
    throw new Error("Свой Java 21 лаунчер качает под Windows. На этой системе его нет.");
  }
  hooks.onLog?.("качаю Java 21");
  const zipPath = path.join(CACHE_DIR, "temurin-21-jre.zip");
  const url = "https://api.adoptium.net/v3/binary/latest/21/ga/windows/x64/jre/hotspot/normal/eclipse?project=jdk";
  await downloadFile(url, zipPath, {
    isCancelled: hooks.isCancelled,
    onProgress: ({ received, total }) => hooks.onProgress?.({ phase: "java", current: received, total, label: "Java 21" }),
  });
  const { default: AdmZip } = await import("adm-zip");
  const zip = new AdmZip(zipPath);
  const dest = path.join(RUNTIME_DIR, "java");
  fs.mkdirSync(dest, { recursive: true });
  zip.extractAllTo(dest, true);
  const java = findJava(dest);
  if (!java) throw new Error("в архиве Java нет java.exe");
  hooks.onLog?.("Java 21 на месте");
  return java;
}

function run(java, args, cwd, onLog) {
  return new Promise((resolve, reject) => {
    const child = spawn(java, args, { cwd, windowsHide: true });
    let tail = "";
    const take = (chunk) => {
      tail = (tail + chunk.toString()).slice(-4000);
      const lines = chunk.toString().split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
      const last = lines.at(-1);
      if (last) onLog?.(last.slice(0, 180));
    };
    child.stdout?.on("data", take);
    child.stderr?.on("data", take);
    child.on("error", reject);
    child.on("exit", (code) => {
      if (code === 0) resolve();
      else reject(new Error(`установщик NeoForge вылетел (${code})\n${tail.slice(-800)}`));
    });
  });
}

// The installer refuses a folder that has never seen the vanilla launcher.
function ensureLauncherProfile(gameDir) {
  fs.mkdirSync(gameDir, { recursive: true });
  const profile = path.join(gameDir, "launcher_profiles.json");
  if (fs.existsSync(profile)) return;
  const now = new Date().toISOString();
  fs.writeFileSync(
    profile,
    JSON.stringify(
      {
        profiles: {
          tbs: {
            created: now,
            icon: "Grass",
            lastUsed: now,
            lastVersionId: "1.21.1",
            name: "TBS",
            type: "custom",
          },
        },
        selectedProfile: "tbs",
        clientToken: "tbs-launcher",
      },
      null,
      2,
    ),
  );
}

export async function ensureNeoForge(javaPath, version, hooks) {
  const id = `neoforge-${version}`;
  const jsonPath = path.join(GAME_DIR, "versions", id, `${id}.json`);
  if (fs.existsSync(jsonPath)) {
    hooks.onLog?.(`NeoForge ${version} уже стоит`);
    return id;
  }
  const jar = path.join(CACHE_DIR, `neoforge-${version}-installer.jar`);
  const url = `https://maven.neoforged.net/releases/net/neoforged/neoforge/${version}/neoforge-${version}-installer.jar`;
  hooks.onLog?.(`качаю NeoForge ${version}`);
  await downloadFile(url, jar, {
    isCancelled: hooks.isCancelled,
    onProgress: ({ received, total }) => hooks.onProgress?.({ phase: "neoforge", current: received, total, label: id }),
  });
  ensureLauncherProfile(GAME_DIR);
  hooks.onLog?.("ставлю NeoForge, это не быстро");
  fs.mkdirSync(GAME_DIR, { recursive: true });
  await run(
    javaPath,
    ["-Djava.awt.headless=true", "-jar", jar, "--installClient", GAME_DIR],
    CACHE_DIR,
    hooks.onLog,
  );
  if (!fs.existsSync(jsonPath)) {
    const versions = path.join(GAME_DIR, "versions");
    const found = fs.existsSync(versions)
      ? fs.readdirSync(versions).find((name) => name.startsWith("neoforge-"))
      : null;
    if (!found) throw new Error("установщик не создал версию NeoForge");
    return found;
  }
  return id;
}
