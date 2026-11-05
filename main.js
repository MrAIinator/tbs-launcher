import fs from "node:fs";
import http from "node:http";
import path from "node:path";
import { spawn } from "node:child_process";
import { clearAccount, DEFAULT_CLIENT_ID, ensureFreshAccount, finishLogin, pollDeviceCode, readAccount, startDeviceCode } from "./lib/auth.js";
import { launchGame } from "./lib/launch.js";
import { mrpackPathFor, queueLauncherUpdate, readInstalled, resolveManifest, writeInstalled } from "./lib/manifest.js";
import { installMrpack } from "./lib/mrpack.js";
import { ensureJava, ensureNeoForge } from "./lib/neoforge.js";
import { CACHE_DIR, DATA_DIR, GAME_DIR, LAUNCHER_DIR } from "./lib/paths.js";
import { ensureVanilla } from "./lib/vanilla.js";

const DEFAULT_SETTINGS = { ramGb: 8, width: 1920, height: 1080, fullscreen: false, clientId: "" };
const settingsPath = path.join(DATA_DIR, "settings.json");

const state = {
  product: "tbs-launcher",
  busy: false,
  phase: "ожидание",
  progress: null,
  error: "",
  log: [],
  login: null,
  manifest: null,
  mods: [],
  updateReady: false,
};

let cancelFlag = false;
let pendingLogin = null;

function safe(text) {
  return String(text).replace(/eyJ[A-Za-z0-9._-]{12,}/g, "[token]").slice(0, 500);
}

function log(text) {
  const line = `${new Date().toLocaleTimeString("ru-RU")}  ${safe(text)}`;
  state.log.push(line);
  if (state.log.length > 80) state.log.shift();
  console.log(line);
}

function readSettings() {
  try {
    const parsed = JSON.parse(fs.readFileSync(settingsPath, "utf8"));
    return sanitizeSettings({ ...DEFAULT_SETTINGS, ...parsed });
  } catch {
    return { ...DEFAULT_SETTINGS };
  }
}

function sanitizeSettings(input) {
  const ramGb = Math.min(24, Math.max(2, Math.round(Number(input.ramGb) || 8)));
  const width = Math.min(7680, Math.max(640, Math.round(Number(input.width) || 1920)));
  const height = Math.min(4320, Math.max(480, Math.round(Number(input.height) || 1080)));
  const clientId = /^[0-9a-fA-F-]{16,80}$/.test(String(input.clientId || "")) ? String(input.clientId) : "";
  return { ramGb, width, height, fullscreen: Boolean(input.fullscreen), clientId };
}

function writeSettings(next) {
  fs.mkdirSync(DATA_DIR, { recursive: true });
  const clean = sanitizeSettings(next);
  fs.writeFileSync(settingsPath, JSON.stringify(clean, null, 2));
  return clean;
}

function cancelled() {
  return cancelFlag;
}

function hooks() {
  return {
    isCancelled: cancelled,
    onLog: log,
    onProgress: (progress) => {
      state.progress = progress;
      if (progress?.phase) state.phase = progress.phase;
    },
  };
}

function publicState() {
  const account = readAccount();
  const manifest = state.manifest;
  return {
    product: state.product,
    busy: state.busy,
    phase: state.phase,
    progress: state.progress,
    error: state.error,
    log: state.log.slice(-12),
    login: state.login
      ? { userCode: state.login.userCode, verificationUri: state.login.verificationUri, message: state.login.message }
      : null,
    account: account?.profile ? { name: account.profile.name, id: account.profile.id } : null,
    settings: readSettings(),
    manifest: manifest
      ? {
          launcherVersion: manifest.launcherVersion,
          packVersion: manifest.packVersion,
          packName: manifest.packName,
          minecraft: manifest.minecraft,
          neoforge: manifest.neoforge,
          server: manifest.server,
          notes: manifest.notes || "",
        }
      : null,
    mods: state.mods,
    updateReady: state.updateReady,
    installed: Boolean(readInstalled()),
  };
}

async function loadMods() {
  const mrpack = path.join(LAUNCHER_DIR, "pack", "pack.mrpack");
  const { default: AdmZip } = await import("adm-zip");
  const Zip = AdmZip?.default || AdmZip;
  const zip = new Zip(mrpack);
  const index = JSON.parse(zip.readAsText("modrinth.index.json"));
  const files = (index.files || [])
    .filter((file) => (file.env?.client || "required") !== "unsupported")
    .map((file) => ({
      name: path.basename(file.path).replace(/\.(jar|zip)$/i, ""),
      kind: file.path.split("/")[0],
      mb: Math.round((file.fileSize || 0) / 104857.6) / 10,
    }));
  for (const entry of zip.getEntries()) {
    if (!entry.entryName.startsWith("overrides/mods/") || !entry.entryName.endsWith(".jar")) continue;
    files.push({
      name: path.basename(entry.entryName).replace(/\.jar$/i, ""),
      kind: "mods",
      mb: Math.round(entry.header.size / 104857.6) / 10,
    });
  }
  state.mods = files;
}

async function refreshManifest() {
  state.manifest = await resolveManifest();
  queueLauncherUpdate(state.manifest, hooks())
    .then((ready) => {
      state.updateReady = ready;
      if (ready) log("обновление лаунчера скачано. перезапусти его");
    })
    .catch((error) => log(`обновление лаунчера не скачалось: ${error.message || error}`));
}

async function play() {
  if (state.busy) return;
  state.busy = true;
  state.error = "";
  cancelFlag = false;
  state.progress = null;
  try {
    if (!state.manifest) await refreshManifest();
    const manifest = state.manifest;
    const settings = readSettings();
    const account = await ensureFreshAccount();
    log(`лицензия: ${account.profile.name}`);
    const javaPath = await ensureJava(hooks());
    await ensureVanilla(manifest.minecraft, hooks());
    const packPath = await mrpackPathFor(manifest, hooks());
    const previous = readInstalled();
    const installed = await installMrpack(packPath, { ...hooks(), previous });
    const versionId = await ensureNeoForge(javaPath, installed.neoforge, hooks());
    writeInstalled({ ...installed, packVersion: manifest.packVersion || installed.packVersion });
    const host = manifest.server?.host;
    const port = Number(manifest.server?.port);
    if (!host || !port) throw new Error("в манифесте нет адреса сервера");
    state.phase = "запуск";
    log(`старт и вход на ${host}:${port}`);
    const child = await launchGame({
      gameDir: GAME_DIR,
      javaPath,
      versionId,
      profile: account.profile,
      accessToken: account.mcToken,
      ramMb: settings.ramGb * 1024,
      width: settings.width,
      height: settings.height,
      fullscreen: settings.fullscreen,
      host,
      port,
    });
    log(`игра запущена (pid ${child.pid || "?"}). это окно можно закрыть`);
    state.phase = "в игре";
  } catch (error) {
    state.error = safe(error?.message || error);
    state.phase = "ошибка";
    log(state.error);
  } finally {
    state.busy = false;
    state.progress = null;
  }
}

function send(res, status, body, type = "application/json; charset=utf-8") {
  const payload = typeof body === "string" ? body : JSON.stringify(body);
  res.writeHead(status, { "Content-Type": type, "Cache-Control": "no-store" });
  res.end(payload);
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    req.on("data", (chunk) => {
      size += chunk.length;
      if (size > 1_000_000) {
        reject(new Error("слишком большой запрос"));
        req.destroy();
        return;
      }
      chunks.push(chunk);
    });
    req.on("end", () => {
      if (!chunks.length) return resolve({});
      try {
        resolve(JSON.parse(Buffer.concat(chunks).toString("utf8")));
      } catch {
        reject(new Error("битый json"));
      }
    });
    req.on("error", reject);
  });
}

function contentType(file) {
  if (file.endsWith(".css")) return "text/css; charset=utf-8";
  if (file.endsWith(".js")) return "text/javascript; charset=utf-8";
  if (file.endsWith(".svg")) return "image/svg+xml";
  return "text/html; charset=utf-8";
}

async function handle(req, res) {
  const url = new URL(req.url || "/", "http://127.0.0.1");
  try {
    if (req.method === "GET" && url.pathname === "/api/state") return send(res, 200, publicState());
    if (req.method === "POST" && url.pathname === "/api/settings") {
      const body = await readBody(req);
      return send(res, 200, { settings: writeSettings(body) });
    }
    if (req.method === "POST" && url.pathname === "/api/login/start") {
      const settings = readSettings();
      const clientId = settings.clientId || DEFAULT_CLIENT_ID;
      const data = await startDeviceCode(clientId);
      pendingLogin = { clientId, deviceCode: data.device_code, interval: Math.max(5, Number(data.interval) || 5) };
      state.login = {
        userCode: data.user_code,
        verificationUri: data.verification_uri,
        message: data.message || "",
      };
      log(`код входа: ${data.user_code}`);
      return send(res, 200, { ok: true });
    }
    if (req.method === "POST" && url.pathname === "/api/login/poll") {
      if (!pendingLogin) return send(res, 200, { pending: false, idle: true });
      const result = await pollDeviceCode(pendingLogin);
      if (!result.done) return send(res, 200, { pending: true });
      const account = await finishLogin({ msa: result.token, clientId: pendingLogin.clientId });
      pendingLogin = null;
      state.login = null;
      log(`лицензия привязана: ${account.profile.name}`);
      return send(res, 200, { pending: false, name: account.profile.name });
    }
    if (req.method === "POST" && url.pathname === "/api/logout") {
      clearAccount();
      pendingLogin = null;
      state.login = null;
      return send(res, 200, { ok: true });
    }
    if (req.method === "POST" && url.pathname === "/api/play") {
      void play();
      return send(res, 200, { ok: true });
    }
    if (req.method === "POST" && url.pathname === "/api/cancel") {
      cancelFlag = true;
      log("отмена");
      return send(res, 200, { ok: true });
    }
    if (req.method === "POST" && url.pathname === "/api/repair") {
      fs.rmSync(path.join(GAME_DIR, "tbs-vanilla.json"), { force: true });
      const versions = path.join(GAME_DIR, "versions");
      if (fs.existsSync(versions)) {
        for (const name of fs.readdirSync(versions)) {
          if (name.startsWith("neoforge-")) fs.rmSync(path.join(versions, name), { recursive: true, force: true });
        }
      }
      log("метки установки сброшены. следующий запуск качает заново то, чего не хватает");
      return send(res, 200, { ok: true });
    }
    if (req.method === "POST" && url.pathname === "/api/quit") {
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end('{"ok":true}');
      setTimeout(() => process.exit(0), 200);
      return;
    }
    if (req.method === "GET") {
      const rel = url.pathname === "/" ? "index.html" : url.pathname.replace(/^\/+/, "");
      if (rel.includes("..")) return send(res, 404, { error: "нет" });
      const file = path.join(LAUNCHER_DIR, "ui", rel);
      if (!file.startsWith(path.join(LAUNCHER_DIR, "ui"))) return send(res, 404, { error: "нет" });
      if (!fs.existsSync(file) || !fs.statSync(file).isFile()) return send(res, 404, "нет", "text/plain");
      return send(res, 200, fs.readFileSync(file), contentType(file));
    }
    return send(res, 404, { error: "нет" });
  } catch (error) {
    state.error = safe(error?.message || error);
    log(state.error);
    if (!res.headersSent) send(res, 500, { error: state.error });
  }
}

function openWindow(url) {
  if (process.platform !== "win32") {
    const child = spawn("xdg-open", [url], { detached: true, stdio: "ignore" });
    child.unref();
    return;
  }
  const candidates = [
    path.join(process.env.LOCALAPPDATA || "", "Microsoft", "Edge", "Application", "msedge.exe"),
    path.join(process.env["PROGRAMFILES(X86)"] || "", "Microsoft", "Edge", "Application", "msedge.exe"),
    path.join(process.env.PROGRAMFILES || "", "Microsoft", "Edge", "Application", "msedge.exe"),
    path.join(process.env.PROGRAMFILES || "", "Google", "Chrome", "Application", "chrome.exe"),
  ];
  for (const exe of candidates) {
    if (exe && fs.existsSync(exe)) {
      const child = spawn(exe, [`--app=${url}`, "--window-size=1200,780"], { detached: true, stdio: "ignore" });
      child.unref();
      return;
    }
  }
  const child = spawn("cmd", ["/c", "start", "", url], { detached: true, stdio: "ignore", windowsHide: true });
  child.unref();
}

async function alreadyRunning(port) {
  try {
    const response = await fetch(`http://127.0.0.1:${port}/api/state`, { signal: AbortSignal.timeout(700) });
    if (!response.ok) return false;
    const data = await response.json();
    return data.product === "tbs-launcher";
  } catch {
    return false;
  }
}

const port = 47821;
if (await alreadyRunning(port)) {
  openWindow(`http://127.0.0.1:${port}/`);
  process.exit(0);
}

fs.mkdirSync(CACHE_DIR, { recursive: true });
fs.mkdirSync(GAME_DIR, { recursive: true });
await loadMods();
void refreshManifest();

const server = http.createServer((req, res) => {
  void handle(req, res);
});
server.on("error", (error) => {
  console.error(error);
  process.exit(1);
});
server.listen(port, "127.0.0.1", () => {
  const url = `http://127.0.0.1:${port}/`;
  log(`лаунчер слушает ${url}`);
  openWindow(url);
});
