import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { downloadFile } from "./download.js";
import { DATA_DIR, safeJoin } from "./paths.js";

function osName() {
  if (process.platform === "win32") return "windows";
  if (process.platform === "darwin") return "osx";
  return "linux";
}

function rulesAllow(rules, features) {
  if (!rules || rules.length === 0) return true;
  let matched = false;
  let allow = false;
  for (const rule of rules) {
    if (rule.os?.name && rule.os.name !== osName()) continue;
    if (rule.os?.arch && rule.os.arch !== process.arch) continue;
    if (rule.features) {
      const ok = Object.entries(rule.features).every(([key, value]) => Boolean(features[key]) === Boolean(value));
      if (!ok) continue;
    }
    matched = true;
    allow = rule.action === "allow";
  }
  return matched ? allow : false;
}

function readJson(gameDir, id) {
  return JSON.parse(fs.readFileSync(path.join(gameDir, "versions", id, `${id}.json`), "utf8"));
}

function mergeVersion(gameDir, id, seen = new Set()) {
  if (seen.has(id)) throw new Error(`цикл версий ${id}`);
  seen.add(id);
  const json = readJson(gameDir, id);
  if (!json.inheritsFrom) return { ...json, jarId: id };
  const parent = mergeVersion(gameDir, json.inheritsFrom, seen);
  return {
    ...parent,
    ...json,
    jarId: parent.jarId,
    libraries: [...(parent.libraries || []), ...(json.libraries || [])],
    arguments: {
      game: [...(parent.arguments?.game || []), ...(json.arguments?.game || [])],
      jvm: [...(parent.arguments?.jvm || []), ...(json.arguments?.jvm || [])],
    },
    assetIndex: json.assetIndex || parent.assetIndex,
    downloads: json.downloads?.client ? json.downloads : parent.downloads,
    mainClass: json.mainClass || parent.mainClass,
    id: json.id || id,
  };
}

function artifactsOf(lib) {
  const out = [];
  if (lib.downloads?.artifact?.path) out.push(lib.downloads.artifact);
  const natives = lib.natives?.[osName()];
  if (natives) {
    const arch = process.arch === "x64" ? "64" : "32";
    const classifier = natives.replace("${arch}", arch);
    const native = lib.downloads?.classifiers?.[classifier];
    if (native?.path) out.push({ ...native, native: true });
  }
  return out;
}

function collect(list, features) {
  const out = [];
  for (const item of list || []) {
    if (typeof item === "string") {
      out.push(item);
      continue;
    }
    if (!rulesAllow(item.rules, features)) continue;
    if (Array.isArray(item.value)) out.push(...item.value);
    else if (item.value) out.push(item.value);
  }
  return out;
}

function subst(value, vars) {
  return value.replace(/\$\{([^}]+)\}/g, (_, key) => (vars[key] == null ? "" : String(vars[key])));
}

async function loadZip() {
  const mod = await import("adm-zip");
  return mod.default?.default || mod.default || mod;
}

async function extractNatives(jarPath, dest) {
  const Zip = await loadZip();
  const zip = new Zip(jarPath);
  fs.mkdirSync(dest, { recursive: true });
  for (const entry of zip.getEntries()) {
    if (entry.isDirectory) continue;
    if (!/\.(dll|so|dylib)$/i.test(entry.entryName)) continue;
    const target = safeJoin(dest, path.basename(entry.entryName));
    fs.writeFileSync(target, entry.getData());
  }
}

export async function buildPlan(gameDir, options) {
  const version = mergeVersion(gameDir, options.versionId);
  const features = {
    has_custom_resolution: !options.fullscreen,
    is_quick_play_multiplayer: true,
  };
  const chosen = new Map();
  for (const lib of version.libraries || []) {
    if (!rulesAllow(lib.rules, {})) continue;
    for (const artifact of artifactsOf(lib)) chosen.set(artifact.path, artifact);
  }

  for (const artifact of chosen.values()) {
    const dest = safeJoin(path.join(gameDir, "libraries"), artifact.path);
    if (artifact.url) {
      await downloadFile(artifact.url, dest, { sha1: artifact.sha1, isCancelled: options.isCancelled });
    } else if (!fs.existsSync(dest)) {
      throw new Error(`нет библиотеки ${artifact.path}`);
    }
  }

  const nativesDir = path.join(gameDir, "versions", options.versionId, "natives");
  fs.mkdirSync(nativesDir, { recursive: true });
  for (const artifact of chosen.values()) {
    if (!artifact.native) continue;
    await extractNatives(safeJoin(path.join(gameDir, "libraries"), artifact.path), nativesDir);
  }

  const classpath = [...chosen.values()].map((artifact) => path.join(gameDir, "libraries", artifact.path));
  const clientJar = path.join(gameDir, "versions", version.jarId, `${version.jarId}.jar`);
  if (!fs.existsSync(clientJar)) throw new Error(`нет ${version.jarId}.jar`);
  classpath.push(clientJar);

  const vars = {
    natives_directory: nativesDir,
    launcher_name: "TBS",
    launcher_version: "1.0.0",
    classpath: classpath.join(path.delimiter),
    classpath_separator: path.delimiter,
    library_directory: path.join(gameDir, "libraries"),
    auth_player_name: options.profile.name,
    version_name: version.id,
    game_directory: gameDir,
    assets_root: path.join(gameDir, "assets"),
    assets_index_name: version.assetIndex?.id || "17",
    auth_uuid: options.profile.id,
    auth_access_token: options.accessToken,
    user_type: "msa",
    version_type: "TBS",
    resolution_width: String(options.width),
    resolution_height: String(options.height),
    quickPlayMultiplayer: `${options.host}:${options.port}`,
    clientid: "tbs-launcher",
    auth_xuid: options.profile.xuid || "0",
  };

  const jvm = collect(version.arguments?.jvm, features).map((arg) => subst(arg, vars));
  const game = collect(version.arguments?.game, features).map((arg) => subst(arg, vars));
  if (options.fullscreen && !game.includes("--fullscreen")) game.push("--fullscreen");
  if (!game.includes("--quickPlayMultiplayer")) game.push("--quickPlayMultiplayer", `${options.host}:${options.port}`);

  const args = [`-Xms${Math.min(1024, options.ramMb)}M`, `-Xmx${options.ramMb}M`, ...jvm, version.mainClass, ...game];
  return { args, mainClass: version.mainClass, versionId: version.id };
}

export async function launchGame(options) {
  const { args } = await buildPlan(options.gameDir, options);
  const argFile = path.join(DATA_DIR, "launch.args");
  const body = args
    .map((arg) => {
      const normalized = process.platform === "win32" ? arg.replaceAll("\\", "/") : arg;
      return /[\s"]/.test(normalized) ? `"${normalized.replaceAll('"', '\\"')}"` : normalized;
    })
    .join("\n");
  fs.writeFileSync(argFile, body);
  const child = spawn(options.javaPath, [`@${argFile}`], {
    cwd: options.gameDir,
    detached: true,
    stdio: "ignore",
    windowsHide: true,
  });
  child.unref();
  return child;
}
