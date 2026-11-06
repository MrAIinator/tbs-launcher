const $ = (id) => document.getElementById(id);
let panel = null;
let pollingLogin = false;

async function api(pathname, options) {
  const response = await fetch(pathname, {
    headers: { "Content-Type": "application/json" },
    ...options,
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error || "ошибка лаунчера");
  return data;
}

function setPanel(name, html) {
  const node = $("panel");
  if (panel === name) {
    panel = null;
    node.classList.add("hidden");
    return;
  }
  panel = name;
  node.innerHTML = html;
  node.classList.remove("hidden");
}

function renderMods(mods) {
  const items = mods
    .map((mod) => `<li><span>${escapeHtml(mod.name)}</span><span class="muted">${mod.mb} МБ</span></li>`)
    .join("");
  setPanel("mods", `<p class="kicker">В СБОРКЕ ${mods.length}</p><ul class="list">${items}</ul>`);
}

function renderSettings(settings) {
  const presets = [
    [1280, 720],
    [1600, 900],
    [1920, 1080],
    [2560, 1440],
    [3840, 2160],
  ];
  const buttons = presets
    .map(([w, h]) => {
      const on = !settings.fullscreen && settings.width === w && settings.height === h;
      return `<button type="button" data-res="${w}x${h}" data-on="${on}">${w}×${h}</button>`;
    })
    .join("");
  setPanel(
    "settings",
    `<p class="kicker" id="ram-label">ОПЕРАТИВКА ${settings.ramGb} ГБ</p>
     <input id="ram" type="range" min="2" max="24" step="1" value="${settings.ramGb}" />
     <p class="muted">Ниже 8 ГБ сборка с шейдерами будет давиться. Потолок 24.</p>
     <p class="kicker">ОКНО</p>
     <div class="actions">${buttons}
       <button type="button" id="full" data-on="${settings.fullscreen}">Полный экран</button>
     </div>
     <p class="kicker">СВОЁ РАЗРЕШЕНИЕ</p>
     <div class="actions">
       <input id="res-w" type="number" min="640" max="7680" value="${settings.width}" aria-label="ширина" />
       <input id="res-h" type="number" min="480" max="4320" value="${settings.height}" aria-label="высота" />
       <button type="button" id="apply-res">ПРИМЕНИТЬ</button>
     </div>
     <p class="kicker">AZURE CLIENT ID</p>
     <input id="client" type="text" value="${escapeHtml(settings.clientId || "")}" placeholder="если Microsoft отвергнет вход" />
     <button type="button" id="save-client">СОХРАНИТЬ ID</button>`,
  );
  const current = { ...settings };
  $("ram").addEventListener("input", async (event) => {
    current.ramGb = Number(event.target.value);
    const label = $("ram-label");
    if (label) label.textContent = `ОПЕРАТИВКА ${current.ramGb} ГБ`;
    await api("/api/settings", { method: "POST", body: JSON.stringify(current) });
  });
  document.querySelectorAll("[data-res]").forEach((button) => {
    button.addEventListener("click", async () => {
      const [w, h] = button.dataset.res.split("x").map(Number);
      const next = await api("/api/settings", {
        method: "POST",
        body: JSON.stringify({ ...current, width: w, height: h, fullscreen: false }),
      });
      renderSettings(next.settings);
    });
  });
  $("full").addEventListener("click", async () => {
    const next = await api("/api/settings", {
      method: "POST",
      body: JSON.stringify({ ...current, fullscreen: !current.fullscreen }),
    });
    renderSettings(next.settings);
  });
  $("apply-res").addEventListener("click", async () => {
    const next = await api("/api/settings", {
      method: "POST",
      body: JSON.stringify({
        ...current,
        width: Number($("res-w").value),
        height: Number($("res-h").value),
        fullscreen: false,
      }),
    });
    renderSettings(next.settings);
  });
  $("save-client").addEventListener("click", async () => {
    const next = await api("/api/settings", {
      method: "POST",
      body: JSON.stringify({ ...current, clientId: $("client").value.trim() }),
    });
    renderSettings(next.settings);
  });
}

function renderUpdates(state) {
  const manifest = state.manifest || {};
  setPanel(
    "updates",
    `<p class="kicker">ОБНОВЛЕНИЯ</p>
     <p>${state.updateReady ? "Обновление уже скачано. Закрой лаунчер и открой снова: он сам себя заменит." : "Ничего качать руками не надо. Лаунчер сам проверяет обновления при запуске."}</p>
     <p>Сейчас: лаунчер ${escapeHtml(manifest.launcherVersion || "?")}, сборка ${escapeHtml(manifest.packVersion || "?")}, NeoForge ${escapeHtml(manifest.neoforge || "?")}.</p>
     <p class="muted">Первый запуск качает игру, Java 21 и моды. Сервер сам не открывается: заходи из меню игры.</p>
     <p class="kicker">ЕСЛИ СОБИРАЕШЬ МОДПАК САМ</p>
     <ol>
       <li>Новый .mrpack на Minecraft 1.21.1 и NeoForge 21.1.49+.</li>
       <li>Замени pack/pack.mrpack в репозитории MrAIinator/tbs-launcher.</li>
       <li>Подними packVersion в manifest.json.</li>
       <li>Игроки докачают разницу на следующем запуске.</li>
     </ol>
     <p class="muted">${escapeHtml(manifest.notes || "")}</p>`,
  );
}

function escapeHtml(value) {
  return String(value).replace(/[&<>"']/g, (char) => {
    if (char === "&") return "\u0026amp;";
    if (char === "<") return "\u0026lt;";
    if (char === ">") return "\u0026gt;";
    if (char === '"') return "\u0026quot;";
    return "\u0026#39;";
  });
}

function paint(state) {
  const manifest = state.manifest || {};
  $("sub").textContent = `ENHANCED ${manifest.packVersion || ""} · NEOFORGE ${manifest.neoforge || ""}`;
  $("account").textContent = state.account ? `${state.account.name} · лицензия есть` : "не привязана";
  $("phase").textContent = state.error || state.phase || "";
  const settings = state.settings || { ramGb: 8, width: 1920, height: 1080, fullscreen: false };
  $("stats").textContent = state.manifest
    ? `${settings.ramGb} ГБ · ${settings.fullscreen ? "полный экран" : `${settings.width}×${settings.height}`} · ${(state.mods || []).length} модов`
    : "";
  $("warn").textContent = settings.ramGb < 8 ? "Мало памяти для этой сборки." : "";
  $("play").disabled = state.busy;
  $("update").disabled = state.busy;
  $("update").textContent = state.updateReady ? "ПЕРЕЗАПУСК" : "ОБНОВИТЬ";
  const progress = state.progress;
  const fill = $("fill");
  if (progress?.total) {
    const ratio = progress.fileTotal ? progress.received / progress.fileTotal : progress.current / progress.total;
    fill.style.width = `${Math.max(0, Math.min(100, ratio * 100))}%`;
  } else if (state.busy) {
    fill.style.width = "35%";
  } else {
    fill.style.width = "0";
  }
  const box = $("login-box");
  if (state.login?.userCode) {
    box.classList.remove("hidden");
    box.innerHTML = `${escapeHtml(state.login.userCode)}<br><a href="${escapeHtml(state.login.verificationUri || "https://www.microsoft.com/link")}">microsoft.com/link</a>`;
  } else {
    box.classList.add("hidden");
  }
}

async function tick() {
  try {
    const state = await api("/api/state");
    paint(state);
    window.__state = state;
  } catch {
    $("phase").textContent = "нет связи с лаунчером";
  }
}

$("play").addEventListener("click", async () => {
  const state = window.__state;
  if (!state?.account) {
    $("phase").textContent = "сначала привяжи лицензию";
    return;
  }
  await api("/api/play", { method: "POST", body: "{}" });
  tick();
});

$("update").addEventListener("click", async () => {
  await api("/api/update", { method: "POST", body: "{}" });
  tick();
});
$("cancel").addEventListener("click", () => api("/api/cancel", { method: "POST", body: "{}" }));
$("repair").addEventListener("click", () => api("/api/repair", { method: "POST", body: "{}" }));

$("login").addEventListener("click", async () => {
  await api("/api/login/start", { method: "POST", body: "{}" });
  if (pollingLogin) return;
  pollingLogin = true;
  while (pollingLogin) {
    await new Promise((resolve) => setTimeout(resolve, 4000));
    try {
      const result = await api("/api/login/poll", { method: "POST", body: "{}" });
      if (!result.pending) {
        pollingLogin = false;
        if (result.idle) break;
      }
    } catch (error) {
      pollingLogin = false;
      $("phase").textContent = error.message;
    }
  }
  tick();
});

$("logout").addEventListener("click", async () => {
  pollingLogin = false;
  await api("/api/logout", { method: "POST", body: "{}" });
  tick();
});

let logWhich = "game";

async function renderLog() {
  setPanel(
    "log",
    `<p class="kicker">ЛОГ ЗАПУСКА</p>
     <div class="actions">
       <button type="button" id="log-game" data-on="true">ИГРА</button>
       <button type="button" id="log-launcher">ЛАУНЧЕР</button>
       <button type="button" id="log-crash">КРАШ</button>
       <button type="button" id="log-refresh" class="ghost">ОБНОВИТЬ</button>
     </div>
     <pre id="logview">загрузка…</pre>`,
  );
  const buttons = { game: $("log-game"), launcher: $("log-launcher"), crash: $("log-crash") };
  async function load() {
    const data = await api("/api/logs");
    const view = $("logview");
    if (!view) return;
    view.textContent = data[logWhich] || "пусто";
    Object.entries(buttons).forEach(([name, button]) => {
      if (button) button.dataset.on = String(name === logWhich);
    });
  }
  Object.entries(buttons).forEach(([name, button]) => {
    button.addEventListener("click", () => {
      logWhich = name;
      load();
    });
  });
  $("log-refresh").addEventListener("click", load);
  await load();
}

$("settings-btn").addEventListener("click", () => {
  const state = window.__state || { settings: {} };
  if (panel === "settings") {
    setPanel("settings", "");
    return;
  }
  renderSettings(state.settings || { ramGb: 8, width: 1920, height: 1080, fullscreen: false, clientId: "" });
});

$("mods-btn").addEventListener("click", () => {
  const state = window.__state || { mods: [] };
  if (panel === "mods") {
    setPanel("mods", "");
    return;
  }
  renderMods(state.mods || []);
});

$("log-btn").addEventListener("click", () => {
  if (panel === "log") {
    panel = null;
    $("panel").classList.add("hidden");
    return;
  }
  renderLog();
});

setInterval(tick, 1000);
tick();

setInterval(() => {
  const label = $("play-label");
  if (!label || matchMedia("(prefers-reduced-motion: reduce)").matches) return;
  const scrambled = "ИГРАТЬ"
    .split("")
    .map((char) => (Math.random() > 0.7 ? "█▓#@"[Math.floor(Math.random() * 4)] : char))
    .join("");
  label.textContent = scrambled;
  label.dataset.text = scrambled;
  setTimeout(() => {
    label.textContent = "ИГРАТЬ";
    label.dataset.text = "ИГРАТЬ";
  }, 140);
}, 2600);
