import fs from "node:fs";
import path from "node:path";
import { DATA_DIR } from "./paths.js";

// Public device-code client published by mcauth3 for third-party launchers.
// No client secret. Override in settings if Microsoft rejects the app.
export const DEFAULT_CLIENT_ID = "4a07b708-b86d-4365-a55f-f4f23ecb85ab";
const SCOPE = "XboxLive.signin offline_access";
const TOKEN_URL = "https://login.microsoftonline.com/consumers/oauth2/v2.0/token";
const ACCOUNT_PATH = path.join(DATA_DIR, "account.json");

const XSTS_ERRORS = {
  2148916233: "У этого Microsoft-аккаунта нет профиля Xbox. Зайди на xbox.com, создай его и повтори.",
  2148916235: "Аккаунт забанен в Xbox Live.",
  2148916236: "Нужно подтверждение возраста на аккаунте Microsoft.",
  2148916238: "Аккаунту нет 18, нужно разрешение взрослого в семье Microsoft.",
};

function accountPathReady() {
  fs.mkdirSync(DATA_DIR, { recursive: true });
}

export function readAccount() {
  try {
    return JSON.parse(fs.readFileSync(ACCOUNT_PATH, "utf8"));
  } catch {
    return null;
  }
}

export function clearAccount() {
  fs.rmSync(ACCOUNT_PATH, { force: true });
}

function saveAccount(account) {
  accountPathReady();
  fs.writeFileSync(ACCOUNT_PATH, JSON.stringify(account), { mode: 0o600 });
}

async function postForm(url, params) {
  const response = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded", Accept: "application/json" },
    body: new URLSearchParams(params),
  });
  const data = await response.json().catch(() => ({}));
  return { ok: response.ok, status: response.status, data };
}

async function postJson(url, body, headers = {}) {
  const response = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json", ...headers },
    body: JSON.stringify(body),
  });
  const data = await response.json().catch(() => ({}));
  return { ok: response.ok, status: response.status, data };
}

export async function startDeviceCode(clientId) {
  const { ok, data } = await postForm("https://login.microsoftonline.com/consumers/oauth2/v2.0/devicecode", {
    client_id: clientId || DEFAULT_CLIENT_ID,
    scope: SCOPE,
  });
  if (!ok) throw new Error(data.error_description || data.error || "Microsoft не выдал код входа");
  return data;
}

export async function pollDeviceCode(pending) {
  const { ok, data } = await postForm(TOKEN_URL, {
    grant_type: "urn:ietf:params:oauth:grant-type:device_code",
    client_id: pending.clientId,
    device_code: pending.deviceCode,
  });
  if (ok) return { done: true, token: data };
  if (data.error === "authorization_pending" || data.error === "slow_down") {
    return { done: false, slow: data.error === "slow_down" };
  }
  throw new Error(data.error_description || data.error || "вход отменён");
}

async function xboxChain(msaAccessToken) {
  const xbl = await postJson("https://user.auth.xboxlive.com/user/authenticate", {
    Properties: {
      AuthMethod: "RPS",
      SiteName: "user.auth.xboxlive.com",
      RpsTicket: `d=${msaAccessToken}`,
    },
    RelyingParty: "http://auth.xboxlive.com",
    TokenType: "JWT",
  });
  if (!xbl.ok) throw new Error("Xbox Live не принял аккаунт");
  const uhs = xbl.data?.DisplayClaims?.xui?.[0]?.uhs;
  if (!uhs || !xbl.data.Token) throw new Error("Xbox Live не вернул токен");

  const xsts = await postJson("https://xsts.auth.xboxlive.com/xsts/authorize", {
    Properties: { SandboxId: "RETAIL", UserTokens: [xbl.data.Token] },
    RelyingParty: "rp://api.minecraftservices.com/",
    TokenType: "JWT",
  });
  if (!xsts.ok) {
    const code = xsts.data?.XErr;
    throw new Error(XSTS_ERRORS[code] || "XSTS отклонил аккаунт");
  }

  const mc = await postJson("https://api.minecraftservices.com/authentication/login_with_xbox", {
    identityToken: `XBL3.0 x=${uhs};${xsts.data.Token}`,
  });
  if (!mc.ok) {
    const raw = JSON.stringify(mc.data || {}).slice(0, 240);
    if (/invalid app|403/i.test(raw) || mc.status === 403) {
      throw new Error("Microsoft не пустил этот лаунчер к лицензии. В настройках вставь свой Azure Client ID и повтори вход.");
    }
    throw new Error("Minecraft не выдал сессию");
  }
  const xuid = xsts.data?.DisplayClaims?.xui?.[0]?.xid || xbl.data?.DisplayClaims?.xui?.[0]?.xid || "0";
  return { ...mc.data, xuid };
}

async function fetchProfile(accessToken) {
  const response = await fetch("https://api.minecraftservices.com/minecraft/profile", {
    headers: { Authorization: `Bearer ${accessToken}` },
  });
  if (response.status === 404) {
    throw new Error("На этом аккаунте нет лицензии Minecraft Java. Пиратка и оффлайн-ник не пройдут.");
  }
  if (!response.ok) throw new Error("Не удалось проверить лицензию");
  const profile = await response.json();
  if (!profile?.id || !profile?.name) throw new Error("Профиль Minecraft пустой");
  return { id: profile.id, name: profile.name };
}

export async function finishLogin({ msa, clientId }) {
  const mc = await xboxChain(msa.access_token);
  const profile = await fetchProfile(mc.access_token);
  profile.xuid = mc.xuid || "0";
  const account = {
    clientId,
    refreshToken: msa.refresh_token,
    mcToken: mc.access_token,
    expiresAt: Date.now() + Number(mc.expires_in || 86400) * 1000,
    profile,
  };
  saveAccount(account);
  return account;
}

async function refreshMsa(account) {
  const { ok, data } = await postForm(TOKEN_URL, {
    grant_type: "refresh_token",
    client_id: account.clientId || DEFAULT_CLIENT_ID,
    refresh_token: account.refreshToken,
    scope: SCOPE,
  });
  if (!ok) {
    clearAccount();
    throw new Error("Сессия Microsoft протухла. Привяжи лицензию ещё раз.");
  }
  return data;
}

export async function ensureFreshAccount() {
  const account = readAccount();
  if (!account?.refreshToken) throw new Error("Сначала привяжи лицензию Minecraft.");
  if (account.mcToken && account.expiresAt - Date.now() > 5 * 60 * 1000) {
    try {
      await fetchProfile(account.mcToken);
      return account;
    } catch {
      /* refresh below */
    }
  }
  const msa = await refreshMsa(account);
  return finishLogin({ msa, clientId: account.clientId || DEFAULT_CLIENT_ID });
}
