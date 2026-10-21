"use strict";
// ytif board: hash-routed pages over one embedded data document.
//   #/verifications?tags=a,b&gates=check,commit   verification list (tag + gate filters)
//   #/verification/<key>                         relation diagram around one verification
//   #/cost                                       cost, hits, and run selection context
//   #/vocabulary                                 built-in and project vocabulary
// Legacy hashes #/checks and #/check/<key> route to the same pages.

const D = JSON.parse(document.getElementById("data").textContent);

// ---- text ----
const T = {
  en: {
    tabVerifications: "Verifications", tabDiagram: "Diagram", tabCost: "Cost & hits", tabVocab: "Vocabulary", langName: "English", language: "Language",
    theme: "Theme", themes: { system: "System", light: "Light", dark: "Dark" },
    meta: (r, v, g) => `${r} · verifications v${v} · generated ${g}`,
    filters: "Filters", clearAll: "Clear all", filterHelp: "A verification stays when it matches any chosen tag in a group, in every group, and any chosen gate.",
    gatesFilter: "Gates", searchList: "Find by impact, verification name, command, or unit", sort: "Sort", sortInv: "Inventory order", sortUnit: "By unit", sortCost: "Highest cost",
    count: (n, t) => `<b>${n}</b> of ${t}`, colImpact: "Impact · verification", colTags: "Tags", colGate: "Gates",
    more: (k, r) => `${k} more · ${r} left`, noMatch: "No verification matches.",
    searchVerification: "Find a verification: impact, name, command, unit", matchCount: (m, n) => `${m} of ${n} match`, firstShown: l => ` · first ${l} shown`,
    recent: "Recent", runsAt: g => `member of ${g}`, accident: "Accident", detection: "Detection", deleteWhen: "Delete when", location: "Location",
    command: "Command", run: "Run", cwd: "Directory", exclusive: "Exclusive", origin: "Defined in",
    relations: "Direct relations", requires: "Requires", ensures: "Ensures", requiredBy: "Required by", ensuredBy: "Ensured by",
    closest: "Closest verifications", byScore: "by score", tags: "Tags", component: "Component", unit: "unit",
    show: n => `Show · ${n} sharing`, hide: "Hide", moreLeft: (k, r) => `${k} more (${r} left)`, noPeers: "No other verification shares this.",
    noVerification: "No verification has this key.", noVerifications: "The ledger has no verifications.",
    omitted: "omitted", omittedReason: r => `omitted · ${r}`, skipPush: "skip-push",
    membershipNote: "Every verification belongs to CI and, unless skip-push, to push. check and commit are independent opt-ins.",
    costTitle: "Cost and hits per verification", noRecords: "No records yet. Gates append them as they run.",
    source: (f, t, n, parts) => `records · ${f} – ${t} · ${n} gate runs (${parts})`,
    gateTotal: g => `${g} gate verification time`, hitsTotal: n => `Hits (${n} inventoried verifications)`, unrecorded: "Verifications without records",
    verificationsHead: "Verifications", sortTotal: "Largest total", sortHits: "Most hits", sortAvg: "Largest average", sortRuns: "Fewest runs",
    colRuns: "Runs", colHits: "Hits", colTotal: "Total", colAvg: "Average",
    costNote: "A hit counts consecutive fails that a pass ends as one. Times are each verification's own run time from history, not present-day membership; builds and processes are in the unit table. An omission is neither a pass nor a zero-duration run.",
    units: "Unit build and process time", times: n => `${n}×`, orphans: "Records of verifications not in the ledger",
    orphanNote: "Verifications since deleted or renamed: what they caught before they left.", lastFail: (t, g) => `last fail ${t} · ${g}`,
    runsTitle: "Run selection context", runsEmpty: "No selection records yet. Older records predate selection tracking and show mode unknown.",
    colWhen: "Run", colMode: "Mode", colScope: "Scope", colSelected: "Selected", colOmit: "Omitted",
    modeFull: "full", modeAffected: "affected", modeUnknown: "mode unknown", runIncomplete: "incomplete", runFailed: "gate failed",
    affectedZero: "affected selection chose nothing", emptyRegistry: "ledger registered nothing",
    fallback: f => `fallback · ${f}`, revisionMismatch: "working tree differed from the recorded tip",
    registered: (r, s) => `${s} of ${r} selected`,
    vocabTitle: "Vocabulary",
    vocabLede: "A tag name is unique across groups, so a tag alone names its group. Every project shares the ytif built-in vocabulary; a project declares its own groups under vocabulary at the top of ytif-verifications.yaml. Select a count to list those verifications.",
    colGroup: "Group", colRule: "Per verification", colTag: "Tag", colLabel: "Label", colOwner: "Owner", colUses: "Uses",
    ruleMany: "Any number", ruleOne: "Exactly one", ownYtif: "ytif built-in", ownProject: "Project",
    when: "When", whenSub: "Member of", whenText: "Not a tag: gate membership (check and commit are opt-ins; every verification belongs to CI and, unless skip-push, to push).",
  },
  ko: {
    tabVerifications: "검증", tabDiagram: "다이어그램", tabCost: "비용·hit", tabVocab: "어휘표", langName: "한국어", language: "언어",
    theme: "테마", themes: { system: "시스템", light: "라이트", dark: "다크" },
    meta: (r, v, g) => `${r} · verifications v${v} · 생성 ${g}`,
    filters: "필터", clearAll: "모두 해제", filterHelp: "같은 묶음 안에서는 하나라도, 묶음끼리는 모두 맞고, 고른 gate 중 하나에는 속해야 남습니다.",
    gatesFilter: "gate", searchList: "impact, 검증 이름, 명령, unit으로 찾기", sort: "정렬", sortInv: "인벤토리 순", sortUnit: "unit 순", sortCost: "비용 큰 순",
    count: (n, t) => `<b>${n}</b>개 / ${t}`, colImpact: "impact · 검증", colTags: "태그", colGate: "gate",
    more: (k, r) => `${k}개 더 보기 · ${r}개 남음`, noMatch: "맞는 검증이 없습니다.",
    searchVerification: "검증 찾기: impact, 이름, 명령, unit", matchCount: (m, n) => `${n}개 중 ${m}개 일치`, firstShown: l => ` · 앞의 ${l}개만 표시`,
    recent: "최근 본 검증", runsAt: g => `${g}에 속함`, accident: "사고", detection: "탐지", deleteWhen: "삭제 조건", location: "위치",
    command: "명령", run: "실행", cwd: "디렉터리", exclusive: "exclusive", origin: "정의 위치",
    relations: "직접 관계", requires: "선행 조건", ensures: "보장 대상", requiredBy: "이 검증을 필요로 함", ensuredBy: "이 검증을 보장함",
    closest: "가장 가까운 검증", byScore: "점수 순", tags: "태그", component: "컴포넌트", unit: "unit",
    show: n => `펼치기 · 같은 검증 ${n}개`, hide: "접기", moreLeft: (k, r) => `${k}개 더 보기 (${r}개 남음)`, noPeers: "이 값을 가진 다른 검증이 없습니다.",
    noVerification: "이 key의 검증이 없습니다.", noVerifications: "ledger에 검증이 없습니다.",
    omitted: "제외", omittedReason: r => `제외 · ${r}`, skipPush: "skip-push",
    membershipNote: "모든 검증은 CI에 속하고, skip-push가 아니면 push에도 속합니다. check와 commit은 각각 지정합니다.",
    costTitle: "검증별 비용과 hit", noRecords: "아직 기록이 없습니다. gate가 실행될 때마다 쌓입니다.",
    source: (f, t, n, parts) => `records · ${f} – ${t} · gate 실행 ${n}회 (${parts})`,
    gateTotal: g => `${g} gate 검증 시간 합`, hitsTotal: n => `hit (ledger 검증 ${n}개)`, unrecorded: "기록이 없는 검증",
    verificationsHead: "검증", sortTotal: "총 시간 큰 순", sortHits: "hit 많은 순", sortAvg: "평균 시간 큰 순", sortRuns: "실행 수 적은 순",
    colRuns: "실행", colHits: "hit", colTotal: "총 시간", colAvg: "평균",
    costNote: "hit는 pass로 끊기는 연속 fail을 1회로 셉니다. 시간은 기록에 남은 검증 자신의 실행 시간이며 현재 소속이 아닙니다. 제외는 pass도 0초 실행도 아닙니다.",
    units: "unit 빌드·프로세스 시간", times: n => `${n}회`, orphans: "ledger에 없는 검증의 기록",
    orphanNote: "지우거나 이름을 바꾼 검증의 기록입니다. 떠나기 전 무엇을 잡았는지 보여 줍니다.", lastFail: (t, g) => `마지막 fail ${t} · ${g}`,
    runsTitle: "실행 선택 기록", runsEmpty: "선택 기록이 없습니다. 이전 기록은 선택 추적 이전이라 모드를 알 수 없습니다.",
    colWhen: "실행", colMode: "모드", colScope: "범위", colSelected: "선택", colOmit: "제외",
    modeFull: "full", modeAffected: "affected", modeUnknown: "모드 미상", runIncomplete: "미완료", runFailed: "gate 실패",
    affectedZero: "affected 선택 결과 없음", emptyRegistry: "ledger 등록 없음",
    fallback: f => `fallback · ${f}`, revisionMismatch: "작업트리가 기록된 tip과 달랐음",
    registered: (r, s) => `${r}개 중 ${s}개 선택`,
    vocabTitle: "어휘표",
    vocabLede: "태그 이름은 묶음이 달라도 겹치지 않아서, 태그 하나로 묶음이 정해집니다. ytif 내장 어휘는 모든 프로젝트가 같이 쓰고, 프로젝트 어휘는 ytif-verifications.yaml 맨 위 vocabulary에 선언합니다. 사용 수를 누르면 그 태그로 걸러진 검증 목록으로 갑니다.",
    colGroup: "묶음", colRule: "검증마다", colTag: "태그", colLabel: "표시", colOwner: "소유", colUses: "사용 수",
    ruleMany: "여러 개 가능", ruleOne: "정확히 1개", ownYtif: "ytif 내장", ownProject: "프로젝트",
    when: "When", whenSub: "소속 gate", whenText: "태그가 아닙니다. gate 소속(check·commit은 지정, 모든 검증은 CI와 skip-push 제외 시 push에 속함)을 씁니다.",
  },
};

// ---- per-viewer conveniences ----
const store = {
  get(k, d) { try { const v = localStorage.getItem("ytif-board." + k); return v ? JSON.parse(v) : d } catch { return d } },
  set(k, v) { try { localStorage.setItem("ytif-board." + k, JSON.stringify(v)) } catch {} },
};
let lang = store.get("lang", (navigator.language || "en").toLowerCase().startsWith("ko") ? "ko" : "en");
const t = (k, ...a) => { const v = T[lang][k]; return typeof v === "function" ? v(...a) : v };
const labelOf = l => l ? (l.text || l[lang] || l.en || l.ko || "") : "";

// ---- helpers ----
const $ = (s, el = document) => el.querySelector(s);
const esc = s => String(s ?? "").replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const fmt = n => Number(n).toLocaleString(lang);
const ms = v => {
  if (v == null) return "–";
  if (v < 1000) return `${fmt(Math.round(v))}ms`;
  const s = v / 1000;
  return `${s.toLocaleString(lang, { maximumFractionDigits: s < 10 ? 2 : s < 100 ? 1 : 0 })}s`;
};
const when = iso => { try { return new Intl.DateTimeFormat(lang, { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(new Date(iso)) } catch { return String(iso ?? "") } };
const shares = (a, b) => a.some(v => b.includes(v));
const href = key => "#/verification/" + encodeURIComponent(key);
const GATES = ["check", "commit", "push", "ci"];

// ---- model ----
const GROUPS = D.groups || [];
const TAG = {}; // tag → {group, label, owner}
for (const g of GROUPS) for (const tg of (g.tags || [])) TAG[tg.name] = { group: g.name, label: tg.label, owner: g.owner };
const groupTitle = g => g.owner === "ytif" ? (g.heading || g.name) : g.name;
const groupSub = g => g.owner === "ytif" ? labelOf(g.label) : "";
const tagText = name => TAG[name] ? labelOf(TAG[name].label) : name;

const VERIFICATIONS = D.verifications || [];
const gatesOf = c => Array.isArray(c.gates) ? c.gates : [];
const BY_KEY = new Map(VERIFICATIONS.map((c, i) => [c.key, i]));
const componentOf = unit => String(unit || "").split("/").slice(0, 2).join("/") || ".";
const titleOf = c => c.impact || (c.is_command && Array.isArray(c.run) ? c.run.join(" ") : "") || c.name;
VERIFICATIONS.forEach((c, i) => {
  c.id = i;
  c.gates = gatesOf(c);
  if (!Array.isArray(c.tags)) c.tags = [];
  c.f = Object.fromEntries(GROUPS.map(g => [g.name, []]));
  for (const tg of c.tags) if (TAG[tg]) c.f[TAG[tg].group].push(tg);
  c.f.component = [componentOf(c.unit)];
  c.f.unit = [c.unit || "."];
});
// inverse relations: who requires or ensures this verification
const REQUIRED_BY = new Map(), ENSURED_BY = new Map();
for (const c of VERIFICATIONS) {
  for (const k of (c.requires || [])) (REQUIRED_BY.get(k) || REQUIRED_BY.set(k, []).get(k)).push(c.id);
  for (const k of (c.ensures || [])) (ENSURED_BY.get(k) || ENSURED_BY.set(k, []).get(k)).push(c.id);
}
const DERIVED = [{ key: "component", weight: 1 }, { key: "unit", weight: 3 }];
const INDEX = new Map(); // "facet:value" → ids
const indexAdd = (k, id) => (INDEX.get(k) || INDEX.set(k, []).get(k)).push(id);
for (const c of VERIFICATIONS) {
  for (const tg of c.tags) indexAdd("tag:" + tg, c.id);
  for (const f of DERIVED) indexAdd(f.key + ":" + c.f[f.key][0], c.id);
}
const uses = tg => (INDEX.get("tag:" + tg) || []).length;

// selection context: recorded mode, fallback, revision, and omissions.
// An omission is distinct from a pass or a zero-duration result.
const SELECTIONS = D.selections || [];
const OMISSIONS = new Map(); // key → [{reason, gate, time}]
for (const s of SELECTIONS) {
  for (const o of (s.omissions || [])) {
    if (!OMISSIONS.has(o.key)) OMISSIONS.set(o.key, []);
    OMISSIONS.get(o.key).push({ reason: o.reason, gate: s.gate, time: s.time });
  }
}
const omissionsOf = key => OMISSIONS.get(key) || [];

const HARM = { "code-execution": "X", "permission-bypass": "P", exposure: "K", "data-loss": "L", misdirection: "W", outage: "B" };
const WARN = new Set(["silent", "irreversible"]);
const hc = c => `var(--h-${HARM[(c.f.what || [])[0]] || "B"})`;
const knownTags = c => GROUPS.flatMap(g => c.f[g.name]);
function chips(c) {
  const harm = (c.f.what || [])[0];
  return `<span class="chips">${knownTags(c).map(tg =>
    `<span class="chip${tg === harm ? " harm" : ""}${WARN.has(tg) ? " warn" : ""}"${tg === harm ? ` style="background:${hc(c)}"` : ""} title="${esc(tg)}">${esc(tagText(tg))}</span>`).join("")}</span>`;
}
function gateBadges(c) {
  const gates = new Set(gatesOf(c));
  return `<span class="gates">${GATES.map(g =>
    `<span class="gbadge${gates.has(g) ? " on" : ""}" title="${esc(g)}">${esc(g)}</span>`).join("")}${c.skip_push ? `<span class="gbadge skip" title="skip-push">skip-push</span>` : ""}${c.is_command ? `<span class="gbadge cmd" title="command">cmd</span>` : ""}</span>`;
}
const scoreOf = (c, o) => c.tags.filter(tg => o.tags.includes(tg)).length +
  DERIVED.reduce((s, f) => s + (c.f[f.key][0] === o.f[f.key][0] ? f.weight : 0), 0);
const rankPeers = (c, ids) => ids.filter(id => id !== c.id).map(id => ({ o: VERIFICATIONS[id], score: scoreOf(c, VERIFICATIONS[id]) }))
  .sort((a, b) => b.score - a.score || a.o.id - b.o.id);

// ---- routing ----
function route() {
  const h = location.hash.replace(/^#/, "") || "/verifications";
  const [path, query] = h.split("?");
  const params = new URLSearchParams(query || "");
  if (path.startsWith("/verification/")) return { page: "diagram", key: decodeURIComponent(path.slice(14)), params };
  if (path.startsWith("/check/")) return { page: "diagram", key: decodeURIComponent(path.slice(7)), params };
  if (path === "/verification" || path === "/check") return { page: "diagram", key: null, params };
  if (path === "/cost") return { page: "cost", params };
  if (path === "/vocabulary") return { page: "vocabulary", params };
  return { page: "verifications", params };
}
const setHash = h => { try { history.replaceState(null, "", h) } catch { location.hash = h } };

function header(r) {
  const tags = GROUPS.reduce((n, g) => n + (g.tags || []).length, 0);
  const last = store.get("sel", null);
  const tabs = [
    ["verifications", "#/verifications", t("tabVerifications"), VERIFICATIONS.length],
    ["diagram", last && BY_KEY.has(last) ? href(last) : "#/verification", t("tabDiagram"), null],
    ["cost", "#/cost", t("tabCost"), null],
    ["vocabulary", "#/vocabulary", t("tabVocab"), tags],
  ];
  $("#tabs").innerHTML = tabs.map(([p, h, label, n]) =>
    `<a href="${h}"${r.page === p ? ` aria-current="page"` : ""}>${esc(label)}${n == null ? "" : ` <span class="count num">${fmt(n)}</span>`}</a>`).join("");
  $("#meta").textContent = t("meta", D.meta.repo, D.meta.version, when(D.meta.generated));
  const themeText = `${t("theme")}: ${t("themes")[theme]}`;
  $("#theme").setAttribute("aria-label", themeText);
  $("#theme").title = themeText;
  $("#theme use").setAttribute("href", THEME_ICON[theme]);
  $("#lang").setAttribute("aria-label", t("language"));
  $("#lang").title = t("language");
  $("#langmenu").innerHTML = Object.keys(T).map(k =>
    `<button type="button" role="menuitemradio" data-lang="${k}" lang="${k}" aria-checked="${k === lang}" tabindex="-1"><svg class="i"><use href="#i-check"/></svg>${esc(T[k].langName)}</button>`).join("");
  document.documentElement.lang = lang;
}

// theme cycles system → light → dark; the head script applies a stored choice before first paint
const THEMES = ["system", "light", "dark"];
const THEME_ICON = { system: "#i-monitor", light: "#i-sun", dark: "#i-moon" };
let theme = document.documentElement.dataset.theme || "system";
$("#theme").addEventListener("click", () => {
  theme = THEMES[(THEMES.indexOf(theme) + 1) % THEMES.length];
  if (theme === "system") delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = theme;
  store.set("theme", theme);
  header(route());
});

const langMenu = {
  items: () => [...$("#langmenu").querySelectorAll("button")],
  open() {
    $("#langmenu").hidden = false;
    $("#lang").setAttribute("aria-expanded", "true");
    (this.items().find(b => b.getAttribute("aria-checked") === "true") || this.items()[0]).focus();
  },
  close(refocus) {
    $("#langmenu").hidden = true;
    $("#lang").setAttribute("aria-expanded", "false");
    if (refocus) $("#lang").focus();
  },
};
$("#lang").addEventListener("click", () => $("#langmenu").hidden ? langMenu.open() : langMenu.close(false));
$("#langmenu").addEventListener("click", e => {
  const b = e.target.closest("button[data-lang]");
  if (!b) return;
  langMenu.close(true);
  if (b.dataset.lang !== lang) { lang = b.dataset.lang; store.set("lang", lang); render() }
});
$("#langmenu").addEventListener("keydown", e => {
  const items = langMenu.items(), i = items.indexOf(document.activeElement);
  if (e.key === "ArrowDown" || e.key === "ArrowUp") {
    e.preventDefault();
    items[(i + (e.key === "ArrowDown" ? 1 : items.length - 1)) % items.length].focus();
  } else if (e.key === "Escape" || e.key === "Tab") langMenu.close(e.key === "Escape");
});

// ---- verifications page ----
const list = { q: "", sort: store.get("sort", "inventory"), shown: 50 };
function filtersFrom(params) {
  const chosen = (params.get("tags") || "").split(",").filter(tg => TAG[tg]);
  const tags = Object.fromEntries(GROUPS.map(g => [g.name, chosen.filter(tg => TAG[tg].group === g.name)]));
  const gates = (params.get("gates") || "").split(",").filter(g => GATES.includes(g));
  return { tags, gates };
}
const passes = (c, f, skip) => GROUPS.every(g => g.name === skip || !f.tags[g.name].length || shares(f.tags[g.name], c.f[g.name])) &&
  (!f.gates.length || shares(f.gates, gatesOf(c)));
const textMatch = (c, q) => !q || [c.name, c.accident, c.detection, c.impact, c.unit, (c.run || []).join(" ")].some(s => String(s || "").toLowerCase().includes(q));

function verificationsPage(r) {
  const f = filtersFrom(r.params);
  $("#page").innerHTML = `<div class="listpage">
    <aside class="filters card" aria-label="${esc(t("filters"))}">
      <div class="head"><h2>${esc(t("filters"))}</h2><a href="#/verifications">${esc(t("clearAll"))}</a></div>
      <p class="note">${esc(t("filterHelp"))}</p>
      <div class="fgroup"><h3>${esc(t("gatesFilter"))}</h3><div class="fchips" id="gatefilters">${GATES.map(g => {
        const n = VERIFICATIONS.filter(c => gatesOf(c).includes(g)).length, on = f.gates.includes(g);
        return `<button type="button" class="fchip" data-gate="${esc(g)}" aria-pressed="${on}">${esc(g)} <span class="n num">${fmt(n)}</span></button>`;
      }).join("")}</div><p class="note">${esc(t("membershipNote"))}</p></div>
      <div id="fgroups"></div>
    </aside>
    <section class="listmain">
      <div class="toolbar">
        <label class="sr" for="q">${esc(t("searchList"))}</label>
        <input id="q" type="search" placeholder="${esc(t("searchList"))}" value="${esc(list.q)}">
        <label class="note" for="sort">${esc(t("sort"))}</label>
        <select id="sort">${[["inventory", "sortInv"], ["unit", "sortUnit"], ["cost", "sortCost"]].map(([v, k]) =>
          `<option value="${v}"${list.sort === v ? " selected" : ""}>${esc(t(k))}</option>`).join("")}</select>
      </div>
      <div class="summary" id="summary"></div>
      <div class="rows card" id="rows"></div>
    </section></div>`;
  const draw = () => {
    const q = list.q.trim().toLowerCase();
    const base = VERIFICATIONS.filter(c => textMatch(c, q));
    $("#fgroups").innerHTML = GROUPS.map(g => {
      const pool = base.filter(c => passes({ ...c }, { tags: Object.fromEntries(GROUPS.map(x => [x.name, x.name === g.name ? [] : f.tags[x.name]])), gates: f.gates }, g.name));
      return `<div class="fgroup"><h3>${esc(groupTitle(g))}${groupSub(g) ? ` · ${esc(groupSub(g))}` : ""}</h3><div class="fchips">${(g.tags || []).map(tg => {
        const n = pool.filter(c => c.f[g.name].includes(tg.name)).length, on = f.tags[g.name].includes(tg.name);
        return `<button type="button" class="fchip" data-tag="${esc(tg.name)}" aria-pressed="${on}"${!n && !on ? " disabled" : ""}>${esc(labelOf(tg.label))} <span class="n num">${fmt(n)}</span></button>`;
      }).join("")}</div></div>`;
    }).join("");
    let rows = base.filter(c => passes(c, f));
    if (list.sort === "unit") rows = [...rows].sort((a, b) => String(a.unit || "").localeCompare(String(b.unit || "")) || a.id - b.id);
    if (list.sort === "cost") rows = [...rows].sort((a, b) => (b.stats?.total_ms || 0) - (a.stats?.total_ms || 0) || a.id - b.id);
    const on = GROUPS.flatMap(g => f.tags[g.name]).concat(f.gates.map(g => `gate:${g}`));
    $("#summary").innerHTML = `<span>${t("count", fmt(rows.length), fmt(VERIFICATIONS.length))}</span>` + on.map(tg => `<span class="chip">${esc(tg.startsWith("gate:") ? tg.slice(5) : tagText(tg))}</span>`).join("");
    const shown = rows.slice(0, list.shown);
    $("#rows").innerHTML = rows.length ? `<div class="rowhead"><span>${esc(t("colImpact"))}</span><span>${esc(t("colTags"))}</span><span>${esc(t("colGate"))}</span></div>` +
      shown.map(c => {
        const omit = !c.stats && omissionsOf(c.key).length ? `<span class="omitnote">${esc(t("omitted"))}</span>` : "";
        return `<a class="row" href="${href(c.key)}"><span class="what"><b>${esc(titleOf(c))}</b><span class="id">${esc(c.name)} · ${esc(c.unit)}${omit ? " · " + omit : ""}</span></span>${chips(c)}${gateBadges(c)}</a>`;
      }).join("") +
      (rows.length > shown.length ? `<div class="morebar"><button type="button" class="btn" id="more">${esc(t("more", fmt(Math.min(50, rows.length - shown.length)), fmt(rows.length - shown.length)))}</button></div>` : "")
      : `<p class="empty">${esc(t(VERIFICATIONS.length ? "noMatch" : "noVerifications"))}</p>`;
  };
  draw();
  $("#q").addEventListener("input", e => { list.q = e.target.value; list.shown = 50; draw() });
  $("#sort").addEventListener("change", e => { list.sort = e.target.value; store.set("sort", list.sort); draw() });
  const syncHash = () => {
    const all = GROUPS.flatMap(x => f.tags[x.name]);
    const qs = [];
    if (all.length) qs.push("tags=" + all.map(encodeURIComponent).join(","));
    if (f.gates.length) qs.push("gates=" + f.gates.map(encodeURIComponent).join(","));
    setHash("#/verifications" + (qs.length ? "?" + qs.join("&") : ""));
  };
  $("#page").addEventListener("click", e => {
    const gt = e.target.closest("[data-gate]");
    if (gt) {
      const g = gt.dataset.gate;
      f.gates = f.gates.includes(g) ? f.gates.filter(x => x !== g) : [...f.gates, g];
      syncHash(); list.shown = 50; draw(); return;
    }
    const b = e.target.closest(".fchip[data-tag]");
    if (!b) return;
    const tg = b.dataset.tag, g = TAG[tg].group;
    f.tags[g] = f.tags[g].includes(tg) ? f.tags[g].filter(x => x !== tg) : [...f.tags[g], tg];
    syncHash(); list.shown = 50; draw();
  });
  $("#rows").addEventListener("click", e => { if (e.target.closest("#more")) { list.shown += 50; draw() } });
}

// ---- diagram page ----
const PAGE = 5, RESULT_LIMIT = 40, TRAIL_LIMIT = 8;
const mm = { open: {}, key: null };
function diagramPage(r) {
  if (!VERIFICATIONS.length) { $("#page").innerHTML = `<p class="empty">${esc(t("noVerifications"))}</p>`; return }
  let key = r.key;
  if (key == null) { const last = store.get("sel", null); key = BY_KEY.has(last) ? last : VERIFICATIONS[0].key; setHash(href(key)) }
  const id = BY_KEY.get(key);
  if (id == null) { $("#page").innerHTML = `<p class="empty">${esc(t("noVerification"))} <code>${esc(key)}</code></p>`; return }
  if (mm.key !== key) { mm.open = { closest: PAGE }; mm.key = key }
  store.set("sel", key);
  const trail = [key, ...store.get("trail", []).filter(k => k !== key && BY_KEY.has(k))].slice(0, TRAIL_LIMIT);
  store.set("trail", trail);
  const c = VERIFICATIONS[id];

  $("#page").innerHTML = `<div class="picker" id="picker">
      <div class="sbox"><label class="sr" for="find">${esc(t("searchVerification"))}</label>
        <input id="find" type="search" role="combobox" aria-expanded="false" aria-controls="results" aria-autocomplete="list" autocomplete="off" placeholder="${esc(t("searchVerification"))}">
        <div class="results" id="results" role="listbox" hidden></div></div>
      <div class="trail">${trail.length > 1 ? `<span class="note">${esc(t("recent"))}</span>` + trail.map(k => {
        const o = VERIFICATIONS[BY_KEY.get(k)];
        return `<button type="button" data-key="${esc(k)}" aria-current="${k === key}" title="${esc(titleOf(o))}"><span class="dot" style="background:${hc(o)}"></span><span>${esc(o.name)}</span></button>`;
      }).join("") : ""}</div>
    </div>
    <div class="mm" id="mm"><svg class="wires" id="wires" aria-hidden="true"></svg>
      <div class="side left" id="left"></div><article class="core" id="core"></article><div class="side right" id="right"></div></div>`;

  const core = $("#core");
  core.style.setProperty("--hc", hc(c));
  const omits = omissionsOf(c.key);
  core.innerHTML = `<span class="eyebrow">${esc((c.gates || []).join(" · ") || t("modeUnknown"))}</span><p class="out">${esc(titleOf(c))}</p>${chips(c)}${gateBadges(c)}<span class="mono note">${esc(c.name)}</span>` +
    (omits.length ? `<span class="note">${esc(t("omittedReason", omits[omits.length - 1].reason || c.key))} · ${esc(omits[omits.length - 1].gate)} · ${esc(when(omits[omits.length - 1].time))}</span>` : "");
  const branch = (k, head, leaves) => `<div class="branch"><div class="bhead" data-wire="core" id="h-${k}">${head}</div><div class="leaves">${leaves.join("")}</div></div>`;
  const locLeaves = [
    `<div class="leaf kv" data-wire="h-loc"><b>unit</b><span class="mono">${esc(c.unit)}</span></div>`,
    `<div class="leaf kv" data-wire="h-loc"><b>runner · gates</b><span class="mono">${esc(c.runner)} · ${esc((c.gates || []).join(" · "))}</span></div>`,
    ...(c.is_command ? [`<div class="leaf kv" data-wire="h-loc"><b>${esc(t("run"))}</b><span class="mono">${esc((c.run || []).join(" "))}</span></div>`] : []),
    ...(c.cwd ? [`<div class="leaf kv" data-wire="h-loc"><b>${esc(t("cwd"))}</b><span class="mono">${esc(c.cwd)}</span></div>`] : []),
    ...(c.exclusive && c.exclusive.length ? [`<div class="leaf kv" data-wire="h-loc"><b>${esc(t("exclusive"))}</b><span class="mono">${esc(c.exclusive.join(", "))}</span></div>`] : []),
    ...(c.origin ? [`<div class="leaf kv" data-wire="h-loc"><b>${esc(t("origin"))}</b><span class="mono">${esc(c.origin)}</span></div>`] : []),
  ];
  const textLeaves = (k, v) => v ? [`<div class="leaf text" data-wire="h-${k}">${esc(v)}</div>`] : [`<div class="leaf" data-wire="h-${k}"><span class="note">–</span></div>`];
  $("#left").innerHTML = [
    branch("accident", esc(t("accident")), textLeaves("accident", c.accident)),
    branch("detection", esc(t("detection")), textLeaves("detection", c.detection)),
    branch("delete", esc(t("deleteWhen")), textLeaves("delete", c.delete_when)),
    branch("loc", esc(t("location")), locLeaves),
  ].join("");

  const peers = (k, ranked, parent) => {
    const n = mm.open[k] || 0, w = `data-wire="${parent}"`;
    if (!ranked.length) return [`<div class="leaf" ${w}><span class="note">${esc(t("noPeers"))}</span></div>`];
    const toggle = `<button type="button" class="toggle" ${w} data-toggle="${esc(k)}" aria-expanded="${n > 0}">${esc(n > 0 ? t("hide") : t("show", fmt(ranked.length)))}</button>`;
    if (!n) return [toggle];
    return [toggle, ...ranked.slice(0, n).map(p => peer(p.o, w, p.score)),
      ...(n < ranked.length ? [`<button type="button" class="more" data-more="${esc(k)}">${esc(t("moreLeft", fmt(Math.min(PAGE, ranked.length - n)), fmt(ranked.length - n)))}</button>`] : [])];
  };
  const peer = (o, w, score) => `<a class="peer" ${w} href="${href(o.key)}" title="${esc(o.name)}"><span class="dot" style="background:${hc(o)}"></span><span class="txt">${esc(titleOf(o))}</span><span class="sc num">${score ?? ""}</span></a>`;
  const sub = (k, text, raw, pid, parent) =>
    `<div class="sub"><span class="tagpill" id="${pid}" data-wire="${parent}" title="${esc(raw)}">${esc(text)}${raw !== text ? `<code>${esc(raw)}</code>` : ""}</span><div class="leaves">${peers(k, rankPeers(c, INDEX.get(k) || []), pid).join("")}</div></div>`;

  const rels = [
    ["requires", (c.requires || []).map(k => BY_KEY.get(k)).filter(i => i != null)],
    ["ensures", (c.ensures || []).map(k => BY_KEY.get(k)).filter(i => i != null)],
    ["requiredBy", REQUIRED_BY.get(c.key) || []],
    ["ensuredBy", ENSURED_BY.get(c.key) || []],
  ].filter(([, ids]) => ids.length);
  const relBranch = rels.length ? [branch("rel", esc(t("relations")), rels.map(([k, ids], i) =>
    `<div class="sub"><span class="tagpill" id="t-rel-${i}" data-wire="h-rel">${esc(t(k))}</span><div class="leaves">${ids.map(x => peer(VERIFICATIONS[x], `data-wire="t-rel-${i}"`)).join("")}</div></div>`))] : [];
  const closest = rankPeers(c, VERIFICATIONS.map(o => o.id)).filter(p => p.score > 0);
  const tags = knownTags(c);
  $("#right").innerHTML = [
    ...relBranch,
    branch("closest", `${esc(t("closest"))} <span>${esc(t("byScore"))}</span>`, peers("closest", closest, "h-closest")),
    branch("tags", `${esc(t("tags"))} <span>${fmt(tags.length)}</span>`, tags.map((tg, i) => sub("tag:" + tg, tagText(tg), tg, `t-tag-${i}`, "h-tags"))),
    ...DERIVED.map(f => branch(f.key, esc(t(f.key)), [sub(f.key + ":" + c.f[f.key][0], c.f[f.key][0], c.f[f.key][0], `t-${f.key}`, `h-${f.key}`)])),
  ].join("");

  $("#mm").addEventListener("click", e => {
    const tg = e.target.closest("[data-toggle]"), m = e.target.closest("[data-more]");
    if (tg) { const k = tg.dataset.toggle; mm.open[k] = mm.open[k] ? 0 : PAGE; render() }
    else if (m) { const k = m.dataset.more; mm.open[k] = (mm.open[k] || 0) + PAGE; render() }
  });
  $(".trail").addEventListener("click", e => { const b = e.target.closest("button[data-key]"); if (b) location.hash = href(b.dataset.key) });
  picker();
  syncStick();
  requestAnimationFrame(drawWires);
}

function picker() {
  const input = $("#find"), box = $("#results");
  let active = -1, shown = [];
  const show = () => {
    const q = input.value.trim().toLowerCase();
    const all = VERIFICATIONS.filter(c => textMatch(c, q));
    shown = all.slice(0, RESULT_LIMIT); active = shown.length ? 0 : -1;
    box.innerHTML = `<div class="rcount">${esc(t("matchCount", fmt(all.length), fmt(VERIFICATIONS.length)))}${all.length > RESULT_LIMIT ? esc(t("firstShown", RESULT_LIMIT)) : ""}</div>` +
      shown.map((c, i) => `<button type="button" class="res" role="option" id="r-${i}" data-key="${esc(c.key)}" aria-selected="${i === active}"><span class="dot" style="background:${hc(c)}"></span><span>${esc(titleOf(c))}<small>${esc(c.name)}</small></span></button>`).join("");
    box.hidden = false; input.setAttribute("aria-expanded", "true");
  };
  const hide = () => { box.hidden = true; input.setAttribute("aria-expanded", "false") };
  const go = k => { hide(); location.hash = href(k) };
  input.addEventListener("input", show);
  input.addEventListener("focus", show);
  input.addEventListener("keydown", e => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault(); if (!shown.length) return;
      active = (active + (e.key === "ArrowDown" ? 1 : -1) + shown.length) % shown.length;
      box.querySelectorAll(".res").forEach((b, i) => b.setAttribute("aria-selected", i === active));
      const el = $(`#r-${active}`); if (el) { el.scrollIntoView({ block: "nearest" }); input.setAttribute("aria-activedescendant", el.id) }
    } else if (e.key === "Enter" && active >= 0) { e.preventDefault(); go(shown[active].key) }
    else if (e.key === "Escape") hide();
  });
  box.addEventListener("mousedown", e => { const b = e.target.closest(".res"); if (b) { e.preventDefault(); go(b.dataset.key) } });
}
document.addEventListener("mousedown", e => {
  if (!$("#langmenu").hidden && !e.target.closest(".menuwrap")) langMenu.close(false);
  const box = $("#results");
  if (box && !e.target.closest(".sbox")) { box.hidden = true; $("#find")?.setAttribute("aria-expanded", "false") }
});

function drawWires() {
  const wrap = $("#mm"), svg = $("#wires");
  if (!wrap || getComputedStyle(svg).display === "none") return;
  const box = wrap.getBoundingClientRect(), paths = [];
  wrap.querySelectorAll("[data-wire]").forEach(el => {
    const from = document.getElementById(el.dataset.wire); if (!from) return;
    const a = from.getBoundingClientRect(), b = el.getBoundingClientRect();
    const toRight = b.left >= a.right - 1;
    const x1 = (toRight ? a.right : a.left) - box.left, y1 = a.top + a.height / 2 - box.top;
    const x2 = (toRight ? b.left : b.right) - box.left, y2 = b.top + Math.min(b.height / 2, 18) - box.top;
    const dx = (x2 - x1) * 0.5;
    paths.push(`<path class="${from.id === "core" ? "core-wire" : ""}" d="M${x1},${y1} C${x1 + dx},${y1} ${x2 - dx},${y2} ${x2},${y2}"/>`);
  });
  svg.innerHTML = paths.join("");
}
function syncStick() { const p = $("#picker"), w = $("#mm"); if (p && w) w.style.setProperty("--stick", `${p.getBoundingClientRect().height + 12}px`) }
let wireFrame = 0;
const scheduleWires = () => { if (!wireFrame) wireFrame = requestAnimationFrame(() => { wireFrame = 0; syncStick(); drawWires() }) };
window.addEventListener("resize", scheduleWires);
window.addEventListener("scroll", scheduleWires, { passive: true });

// ---- cost page ----
const cost = { sort: store.get("costSort", "total"), units: 12, runs: 12 };
function modeText(s) {
  if (!s || !s.known) return t("modeUnknown");
  if (s.effective_mode === "affected") return t("modeAffected");
  if (s.effective_mode === "full") return t("modeFull");
  return s.effective_mode || t("modeUnknown");
}
function runStatus(s) {
  const parts = [];
  if (s && s.known && !s.completed) parts.push(t("runIncomplete"));
  if (s && s.outcome === "fail") parts.push(t("runFailed"));
  return parts.length ? " · " + parts.map(esc).join(" · ") : "";
}
function costPage() {
  const m = D.meta, gates = GATES.filter(g => m.attempts && m.attempts[g] != null).concat(GATES.filter(g => !(m.attempts && m.attempts[g] != null))).filter((g, i, a) => a.indexOf(g) === i);
  const ordered = ["check", "commit", "push", "ci"].filter(g => gates.includes(g)).concat(gates.filter(g => !["check", "commit", "push", "ci"].includes(g)));
  const gateTotal = g => VERIFICATIONS.reduce((s, c) => s + (c.stats?.by_gate[g] || 0), 0);
  const gateTimed = g => VERIFICATIONS.reduce((s, c) => s + (c.stats?.by_gate_timed?.[g] || 0), 0);
  const hits = VERIFICATIONS.reduce((s, c) => s + (c.stats?.hits || 0), 0);
  const runsTotal = Object.values(m.attempts || {}).reduce((s, n) => s + n, 0);
  const source = m.from ? t("source", when(m.from), when(m.to), fmt(runsTotal), ordered.filter(g => m.attempts[g] != null).map(g => `${g} ${fmt(m.attempts[g])}`).join(" · ")) : t("noRecords");
  $("#page").innerHTML = `<div class="pagehead"><h1>${esc(t("costTitle"))}</h1><span class="note">${esc(source)}</span></div>
    <div class="stats">${ordered.filter(g => m.attempts[g] != null).map(g => `<div class="stat card"><span>${esc(t("gateTotal", g))}</span><b class="num">${gateTimed(g) ? ms(gateTotal(g)) : "–"}</b></div>`).join("")}
      <div class="stat card"><span>${esc(t("hitsTotal", fmt(VERIFICATIONS.length)))}</span><b class="num">${fmt(hits)}</b></div>
      <div class="stat card"><span>${esc(t("unrecorded"))}</span><b class="num">${fmt(VERIFICATIONS.filter(c => !c.stats).length)}</b></div></div>
    <section class="sechead"><h2>${esc(t("verificationsHead"))}</h2><span><label class="note" for="csort">${esc(t("sort"))}</label>
      <select id="csort">${[["total", "sortTotal"], ["hits", "sortHits"], ["avg", "sortAvg"], ["runs", "sortRuns"]].map(([v, k]) =>
        `<option value="${v}"${cost.sort === v ? " selected" : ""}>${esc(t(k))}</option>`).join("")}</select></span></section>
    <div class="card scroll"><div style="min-width: 980px" id="costrows"></div></div>
    <p class="note">${esc(t("costNote"))}</p>
    <section style="display:flex;flex-direction:column;gap:10px"><h2>${esc(t("runsTitle"))}</h2><div class="card" id="runs"></div></section>
    <div class="split">
      <section style="flex: 3 1 520px"><h2>${esc(t("units"))}</h2><div class="card" id="units"></div></section>
      <section style="flex: 2 1 360px"><h2>${esc(t("orphans"))}</h2><p class="note">${esc(t("orphanNote"))}</p><div class="card" id="orphans"></div></section>
    </div>`;
  const draw = () => {
    const avg = c => c.stats?.timed ? c.stats.total_ms / c.stats.timed : -1;
    const key = { total: c => -(c.stats?.total_ms ?? -1), hits: c => -(c.stats?.hits ?? -1), avg: c => -avg(c), runs: c => c.stats?.runs ?? Infinity }[cost.sort];
    const rows = [...VERIFICATIONS].sort((a, b) => key(a) - key(b) || a.id - b.id);
    const max = Math.max(1, ...VERIFICATIONS.map(c => c.stats?.total_ms || 0));
    $("#costrows").innerHTML = `<div class="costrow head"><span>${esc(t("colImpact"))}</span><span class="r">${esc(t("colRuns"))}</span><span class="r">${esc(t("colHits"))}</span><span class="tothead">${esc(t("colTotal"))}<span class="legend">${ordered.map((g, i) => `<span><i style="background:${gateColor(i)}"></i>${esc(g)}</span>`).join("")}</span></span><span class="r">${esc(t("colAvg"))}</span></div>` +
      rows.map(c => {
        const s = c.stats, total = s?.total_ms || 0;
        // one segment per gate, each scaled to the largest total, so length reads as time from history
        const split = ordered.map((g, i) => `<span style="width:${(s?.by_gate[g] || 0) / max * 100}%;background:${gateColor(i)}"></span>`).join("");
        const omit = !s && omissionsOf(c.key).length ? ` · ${esc(t("omitted"))}` : "";
        const gateTitle = s ? esc(ordered.map(g => `${g} ${(s.by_gate_timed?.[g] ? ms(s.by_gate[g] || 0) : "–")}`).join(" · ")) : "";
        return `<a class="costrow num" href="${href(c.key)}"><span class="what"><b>${esc(titleOf(c))}</b><span>${esc(c.name)}${omit}</span></span>
          <span class="r">${s ? fmt(s.runs) : "–"}</span><span class="r">${s ? fmt(s.hits) : "–"}</span>
          <span class="total"${s ? ` title="${gateTitle}"` : ""}><span class="bar">${split}</span><em>${s?.timed ? ms(total) : "–"}</em></span>
          <span class="r">${s?.timed ? ms(avg(c)) : "–"}</span></a>`;
      }).join("");
    const runs = [...SELECTIONS].sort((a, b) => new Date(b.time) - new Date(a.time));
    $("#runs").innerHTML = runs.length ? runs.slice(0, cost.runs).map(s => {
      const sel = (s.selected || []).length, reg = s.registered ?? (s.eligible || []).length;
      const state = !s.known ? "" : (reg === 0 ? t("emptyRegistry") : (sel === 0 && s.effective_mode === "affected" ? t("affectedZero") : t("registered", fmt(reg), fmt(sel))));
      const detail = [s.base || s.head ? `base ${s.base || "–"} · head ${s.head || "–"}` : "", state,
        (s.omissions || []).length ? `${t("colOmit")} ${fmt(s.omissions.length)}` : ""].filter(Boolean).join(" · ");
      return `<div class="runrow"><span class="mono">${esc(when(s.time))} · ${esc(s.gate || "")}${s.context ? " · " + esc(s.context) : ""}</span>
        <span>${esc(modeText(s))}${s.requested_mode && s.requested_mode !== s.effective_mode ? esc(` (${s.requested_mode} → ${s.effective_mode})`) : ""}${s.fallback ? " · " + esc(t("fallback", s.fallback)) : ""}${s.revision_mismatch ? " · " + esc(t("revisionMismatch")) : ""}${runStatus(s)}</span>
        <span class="note">${esc(detail)}</span></div>`;
    }).join("") + (runs.length > cost.runs ? `<div class="morebar"><button type="button" class="btn" id="rmore">${esc(t("more", fmt(Math.min(12, runs.length - cost.runs)), fmt(runs.length - cost.runs)))}</button></div>` : "")
      : `<p class="empty">${esc(t("runsEmpty"))}</p>`;
    const invs = D.invocations || [], umax = Math.max(1, ...invs.map(v => v.total_ms));
    $("#units").innerHTML = invs.length ? invs.slice(0, cost.units).map(v => `<div class="unitrow num"><span class="mono">${esc(v.unit || v.runner)}</span><span class="note">${esc(v.what)}</span><span class="r">${esc(t("times", fmt(v.runs)))}</span>
        <span class="total"><span class="bar"><span style="width:${v.total_ms / umax * 100}%;background:var(--accent)"></span></span><em>${v.timed ? ms(v.total_ms) : "–"}</em></span></div>`).join("") +
      (invs.length > cost.units ? `<div class="morebar"><button type="button" class="btn" id="umore">${esc(t("more", fmt(Math.min(12, invs.length - cost.units)), fmt(invs.length - cost.units)))}</button></div>` : "")
      : `<p class="empty">${esc(t("noRecords"))}</p>`;
    $("#orphans").innerHTML = (D.orphans || []).length ? D.orphans.map(o => {
      const [runner, unit, ...name] = String(o.key).split(":");
      return `<div class="orphan"><span class="mono">${esc(name.join(":"))}</span><span class="mono note">${esc(runner)} · ${esc(unit)}</span>
        <span class="num">${esc(t("colRuns"))} <b>${fmt(o.runs)}</b> · ${esc(t("colHits"))} <b>${fmt(o.hits)}</b>${o.last_fail ? ` · ${esc(t("lastFail", when(o.last_fail), o.last_fail_gate))}` : ""}</span></div>`;
    }).join("") : `<p class="empty">–</p>`;
  };
  draw();
  $("#csort").addEventListener("change", e => { cost.sort = e.target.value; store.set("costSort", cost.sort); draw() });
  $("#page").addEventListener("click", e => {
    if (e.target.closest("#umore")) { cost.units += 12; draw() }
    else if (e.target.closest("#rmore")) { cost.runs += 12; draw() }
  });
}
const gateColor = i => ["var(--accent)", "var(--accent-2)", "var(--muted)", "var(--warn-fg)"][i] || "var(--muted)";

// ---- vocabulary page ----
function vocabularyPage() {
  const own = o => `<span class="own ${o}">${esc(t(o === "ytif" ? "ownYtif" : "ownProject"))}</span>`;
  const rows = GROUPS.flatMap(g => (g.tags || []).map((tg, i) => `<tr${i ? "" : ` class="first"`}>${i ? "" :
    `<td class="grp" rowspan="${g.tags.length}">${esc(groupTitle(g))}${groupSub(g) ? `<span>${esc(groupSub(g))}</span>` : ""}</td><td class="rule" rowspan="${g.tags.length}">${esc(t(g.exactly_one ? "ruleOne" : "ruleMany"))}</td>`}
    <td><code>${esc(tg.name)}</code></td><td>${esc(labelOf(tg.label))}</td><td>${own(g.owner)}</td>
    <td class="r num"><a href="#/verifications?tags=${encodeURIComponent(tg.name)}">${fmt(uses(tg.name))}</a></td></tr>`)).join("");
  $("#page").innerHTML = `<div class="pagehead"><h1>${esc(t("vocabTitle"))}</h1></div><p class="lede">${esc(t("vocabLede"))}</p>
    <div class="card scroll"><table style="min-width: 760px"><thead><tr><th>${esc(t("colGroup"))}</th><th>${esc(t("colRule"))}</th><th>${esc(t("colTag"))}</th><th>${esc(t("colLabel"))}</th><th>${esc(t("colOwner"))}</th><th class="r">${esc(t("colUses"))}</th></tr></thead>
    <tbody>${rows}<tr class="first"><td class="grp">${esc(t("when"))}<span>${esc(t("whenSub"))}</span></td><td colspan="5" class="note">${esc(t("whenText"))}</td></tr></tbody></table></div>`;
}

// ---- render ----
function render() {
  const r = route();
  header(r);
  ({ verifications: verificationsPage, checks: verificationsPage, diagram: diagramPage, cost: costPage, vocabulary: vocabularyPage })[r.page === "checks" ? "checks" : r.page](r);
}
window.addEventListener("hashchange", () => { render(); if (route().page === "diagram") window.scrollTo(0, 0) });
render();
