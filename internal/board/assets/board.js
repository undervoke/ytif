"use strict";
// ytif board: hash-routed pages over one embedded data document.
//   #/verifications?tags=a,b&gates=check,commit&q=&runner=&hit=&time=&state=&file=&group=&view=
//                                                verifications and process time
//   #/verification/<key>                         relation diagram around one verification
//   #/vocabulary                                 built-in and project vocabulary
// Legacy hashes #/checks, #/check/<key>, and #/cost route to the same pages.
// The runner param names the runner filter in the verification view and the
// selected runner or unit in the process view.

const D = JSON.parse(document.getElementById("data").textContent);
const META = D.meta || {};
const CRITERIA = META.criteria || { average: {}, hits: { days: 365 }, recent_fail_days: 30 };

// ---- text ----
const T = {
  en: {
    tabVerifications: "Verifications", tabDiagram: "Diagram", tabVocab: "Vocabulary", langName: "English", language: "Language",
    theme: "Theme", themes: { system: "System", light: "Light", dark: "Dark" },
    meta: (r, v, g) => `${r} · verifications v${v} · generated ${g}`,
    title: "Verifications",
    srcLocal: (f, t, n) => `local · ${f} – ${t} · ${n} runs`, srcLocalEmpty: "local · no records",
    srcCI: (repo, range, n) => `CI · ${repo}${range ? ` · ${range}` : ""}${n ? ` · ${n} runs` : ""}`,
    srcSynced: t => `synced ${t}`, srcPending: n => `${n} pending`,
    srcOffline: "sync failed: offline", srcNoCredentials: "sync failed: no credentials", srcNoArtifact: "no artifact",
    srcEvicted: (date, id) => `retention-limited history · eviction boundary ${date}${id ? ` / artifact ${id}` : ""}`,
    srcShort: n => `observed history shorter than the ${n}-day hit window`,
    srcLegacy: size => `legacy file ${size} (outside the managed cap)`,
    srcOverage: size => `managed storage ${size} over budget`, srcMaintenance: error => `maintenance deferred: ${error}`,
    gateTitle: "Verification time per gate", avg: "Average", branch: "Branch", noBranch: "(no branch)", noItem: "(no item)",
    criteriaAvg: "Average", criteriaAvgText: "Mean of the last N timed results per verification and gate, and per process and gate.",
    criteriaBranch: "Branch", criteriaBranchText: "Each gate's latest run on the current branch: the sum of its timed results.",
    criteriaHit: "Hit", criteriaHitText: "A fail counts once; fails until the next pass do not count.",
    criteriaHitCI: "ci counts the received CI records.", criteriaHitLast: (g, n) => `${g} counts its latest ${n} runs`,
    viewVerif: "Verifications", viewProcess: "Process time",
    filesTitle: "Ledger files", filesFind: "Find a path", procAll: "All", procTitle: "Runner · unit", shared: "Shared", procFind: "Find a runner or unit", files: n => `${n} ledger files`,
    sort: "Sort", sortAvg: "Largest average sum", sortInv: "Inventory order", sortHits: "Most hits", sortGate: g => `Largest ${g} average`,
    grouping: "Grouping", groupVerification: "Individual verification", groupUnit: "Execution unit",
    countAll: n => `Verifications ${n}`, countMatch: n => `matching ${n}`, countProc: n => `Processes ${n}`,
    filters: "Filters", applied: "Applied", clearAll: "Clear all", remove: l => `Remove ${l}`,
    searchList: "Find by impact, accident, detection, verification name, command, or unit",
    gates: "Gates", hitGroup: "Hit", hitYes: "yes", hitNo: "no",
    avgGroup: "Average sum", atLeast: v => `≥ ${v}`,
    stateGroup: "State", stateNoSample: "no time samples", stateBranch: "ran on this branch", stateFail: `failed in the last ${CRITERIA.recent_fail_days} days`,
    membershipNote: "Every verification belongs to CI and, unless skip-push, to push. check and commit are independent opt-ins.",
    colImpact: "Impact · verification", colUnit: "Execution unit", colWhat: "Task · runner · unit", colAvg: "Average sum",
    more: (k, r) => `${k} more · ${r} left`,
    noMatch: "No verification matches.", noVerifications: "The ledger has no verifications.",
    noProcesses: "No process matches.", noRecords: "No records yet. Gates append them as they run.",
    tags: "Tags", openDiagram: "Open in diagram →",
    unitKey: (runner, n) => `${runner} · ${n} verifications`,
    run: "Run", cwd: "Directory", exclusive: "Exclusive", origin: "Defined in",
    location: "Location",
    railLabel: "Verification list", railOpen: "Open the list", railClose: "Collapse the list",
    railFilter: "Filter", railGroup: "Grouping", railByFile: "By file", railByRunner: "By command",
    railSearch: "Filter by name, execution unit, or wording", railEmpty: "Nothing matches the filter.",
    railCount: (a, s) => `${s} of ${a}`, noTimeSamples: "No time samples", clearTags: "Clear tags",
    viewLabel: "View",
    relations: "Direct relations", requires: "Requires", ensures: "Ensures", requiredBy: "Required by", ensuredBy: "Ensured by",
    component: "Component", unit: "unit",
    show: n => `Show · ${n} sharing`, hide: "Hide", moreLeft: (k, r) => `${k} more (${r} left)`, noPeers: "No other verification shares this.",
    noVerification: "No verification has this key.", omittedReason: r => `omitted · ${r}`,
    diagramDetection: "How", diagramImpact: "Worst case", diagramAccident: "What", diagramDelete: "Removal condition",
    diagramCommand: "Command it runs", diagramVerification: "Verification", diagramShowAll: "Show all", diagramCollapse: "Collapse", diagramUnit: "Execution unit",
    diagramCost: "Cost", diagramBasis: "Record criteria", diagramLocal: "Local records", diagramUnsynced: "not synced yet",
    diagramSamples: n => `${n} timed samples`, diagramNoSample: "No timed samples", diagramNotMember: "Not in this gate",
    diagramCostHint: "Mean (hit) · Hover a cell for its timed sample count", diagramProcess: "View in process time →",
    diagramFileHint: "Select the file to list its verifications and those in its included files",
    diagramTagHint: "Open verifications filtered by this tag", diagramPeerMeta: (sum, hits) => `Average sum ${sum} · hit ${hits}`,
    diagramBranchHint: "Has results from a run on the current branch", diagramFailHint: `Failed within the last ${CRITERIA.recent_fail_days} days across all branches. This does not mean it is still failing.`,
    diagramNoSampleHint: "No timed results in its current member gates",
    diagramRequiresHint: "Verifications this verification depends on", diagramRequiredByHint: "Verifications that depend on this verification",
    diagramEnsuresHint: "Verifications this verification keeps working", diagramEnsuredByHint: "Verifications that keep this verification working",
    vocabTitle: "Vocabulary",
    vocabLede: "A tag name is unique across groups, so a tag alone names its group. Every project shares the ytif built-in vocabulary; a project declares its own groups under vocabulary at the top of ytif-verifications.yaml. Select a count to list those verifications.",
    colGroup: "Group", colRule: "Per verification", colTag: "Tag", colLabel: "Label", colOwner: "Owner", colUses: "Uses",
    ruleMany: "Any number", ruleOne: "Exactly one", ownYtif: "ytif built-in", ownProject: "Project",
    when: "When", whenSub: "Member of", whenText: "Not a tag: gate membership (check and commit are opt-ins; every verification belongs to CI and, unless skip-push, to push).",
  },
  ko: {
    tabVerifications: "검증", tabDiagram: "다이어그램", tabVocab: "어휘표", langName: "한국어", language: "언어",
    theme: "테마", themes: { system: "시스템", light: "라이트", dark: "다크" },
    meta: (r, v, g) => `${r} · verifications v${v} · 생성 ${g}`,
    title: "검증",
    srcLocal: (f, t, n) => `local · ${f} – ${t} · ${n}회`, srcLocalEmpty: "local · 기록 없음",
    srcCI: (repo, range, n) => `CI · ${repo}${range ? ` · ${range}` : ""}${n ? ` · ${n}회` : ""}`,
    srcSynced: t => `동기화 ${t}`, srcPending: n => `대기 ${n}`,
    srcOffline: "동기화 실패: 오프라인", srcNoCredentials: "동기화 실패: credentials 없음", srcNoArtifact: "artifact 없음",
    srcEvicted: (date, id) => `보존 한도로 줄어든 기록 · 정리 경계 ${date}${id ? ` / artifact ${id}` : ""}`,
    srcShort: n => `관측된 기록 범위가 hit 기간 ${n}일보다 짧음`,
    srcLegacy: size => `기존 파일 ${size} (관리 용량 한도와 별도)`,
    srcOverage: size => `관리 기록 용량 ${size} 초과`, srcMaintenance: error => `정리 보류: ${error}`,
    gateTitle: "gate별 검증 시간 합", avg: "평균", branch: "branch 기준", noBranch: "(branch 없음)", noItem: "(항목 없음)",
    criteriaAvg: "평균", criteriaAvgText: "검증×gate별, 프로세스×gate별 시간이 기록된 최근 N회 평균",
    criteriaBranch: "branch 기준", criteriaBranchText: "현재 branch에서 gate별 마지막 실행 1회의 검증 시간 합",
    criteriaHit: "hit", criteriaHitText: "fail을 1회로 세고, 다음 pass까지의 fail은 세지 않음",
    criteriaHitCI: "ci는 받은 CI 기록 기준", criteriaHitLast: (g, n) => `${g}은 최근 ${n}회 실행 기준`,
    viewVerif: "검증", viewProcess: "프로세스 시간",
    filesTitle: "ledger 파일", filesFind: "경로 찾기", procAll: "전체", procTitle: "runner · 실행 단위", shared: "공용", procFind: "runner·실행 단위 찾기", files: n => `${n}개`,
    sort: "정렬", sortAvg: "평균 합 큰 순", sortInv: "인벤토리 순", sortHits: "hit 많은 순", sortGate: g => `${g} 평균 큰 순`,
    grouping: "묶음", groupVerification: "개별 검증", groupUnit: "실행 단위",
    countAll: n => `검증 ${n}`, countMatch: n => `조건에 맞는 검증 ${n}`, countProc: n => `프로세스 ${n}`,
    filters: "필터", applied: "적용", clearAll: "모두 해제", remove: l => `${l} 해제`,
    searchList: "영향, 사고, 탐지, 검증 이름, 명령, 실행 단위로 찾기",
    gates: "gate", hitGroup: "hit", hitYes: "있음", hitNo: "없음",
    avgGroup: "평균 합", atLeast: v => `${v} 이상`,
    stateGroup: "상태", stateNoSample: "시간 표본 없음", stateBranch: "이 branch에서 실행됨", stateFail: `최근 ${CRITERIA.recent_fail_days}일 fail`,
    membershipNote: "모든 검증은 CI에 속하고, skip-push가 아니면 push에도 속합니다. check와 commit은 각각 지정합니다.",
    colImpact: "영향 · 검증", colUnit: "실행 단위", colWhat: "작업 · runner · 실행 단위", colAvg: "평균 합",
    more: (k, r) => `${k}개 더 보기 · ${r}개 남음`,
    noMatch: "조건에 맞는 검증이 없습니다.", noVerifications: "ledger에 검증이 없습니다.",
    noProcesses: "조건에 맞는 프로세스가 없습니다.", noRecords: "아직 기록이 없습니다. gate가 실행될 때마다 쌓입니다.",
    tags: "태그", openDiagram: "다이어그램에서 보기 →",
    unitKey: (runner, n) => `${runner} · 검증 ${n}`,
    run: "실행", cwd: "디렉터리", exclusive: "exclusive", origin: "정의 위치",
    location: "위치",
    railLabel: "검증 목록", railOpen: "목록 펼치기", railClose: "목록 접기",
    railFilter: "필터", railGroup: "묶음", railByFile: "파일 별", railByRunner: "실행 명령 별",
    railSearch: "이름, 실행 단위, 문구로 거르기", railEmpty: "거른 결과가 없습니다.",
    railCount: (a, s) => `${a}개 중 ${s}개`, noTimeSamples: "시간 표본 없음", clearTags: "태그 모두 해제",
    viewLabel: "보기",
    relations: "직접 관계", requires: "선행 조건", ensures: "보장 대상", requiredBy: "이 검증을 필요로 함", ensuredBy: "이 검증을 보장함",
    component: "컴포넌트", unit: "unit",
    show: n => `펼치기 · 같은 검증 ${n}개`, hide: "접기", moreLeft: (k, r) => `${k}개 더 보기 (${r}개 남음)`, noPeers: "이 값을 가진 다른 검증이 없습니다.",
    noVerification: "이 key의 검증이 없습니다.", omittedReason: r => `제외 · ${r}`,
    diagramDetection: "어떻게", diagramImpact: "최악의 경우", diagramAccident: "무엇을", diagramDelete: "제거 조건",
    diagramCommand: "실행하는 명령", diagramVerification: "검증", diagramShowAll: "전체 보기", diagramCollapse: "접기", diagramUnit: "실행 단위",
    diagramCost: "비용", diagramBasis: "기록 기준", diagramLocal: "로컬 기록", diagramUnsynced: "아직 동기화하지 않음",
    diagramSamples: n => `시간 표본 ${n}회`, diagramNoSample: "시간 표본 없음", diagramNotMember: "이 gate 소속 아님",
    diagramCostHint: "평균 (hit) · 칸에 올리면 시간 표본 수", diagramProcess: "프로세스 시간에서 보기 →",
    diagramFileHint: "파일을 누르면 이 파일과 하위 파일의 검증 목록",
    diagramTagHint: "이 태그로 걸러진 검증 목록으로", diagramPeerMeta: (sum, hits) => `평균 합 ${sum} · hit ${hits}`,
    diagramBranchHint: "현재 branch의 실행에 결과가 있음", diagramFailHint: `모든 branch의 기록에서 최근 ${CRITERIA.recent_fail_days}일 안에 fail이 있음. 지금도 실패 중이라는 뜻은 아님`,
    diagramNoSampleHint: "지금 소속된 gate에 시간이 기록된 결과가 없음",
    diagramRequiresHint: "이 검증이 의존하는 검증", diagramRequiredByHint: "이 검증에 의존하는 검증",
    diagramEnsuresHint: "이 검증이 계속 동작하도록 지켜 주는 검증", diagramEnsuredByHint: "이 검증을 지켜 주는 검증",
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
const day = iso => { const d = new Date(iso); return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}` };
const shares = (a, b) => a.some(v => b.includes(v));
const href = key => "#/verification/" + encodeURIComponent(key);
const GATES = ["check", "commit", "push", "ci"];
const AND = new Set(["time", "state"]); // these groups need every chosen condition, others need any
const gateColor = g => ({ check: "var(--accent)", commit: "var(--accent-2)", push: "var(--muted)", ci: "var(--warn-fg)" }[g] || "var(--muted)");
const splitParam = s => String(s || "").split(",").filter(Boolean);

// ---- model ----
const GROUPS = D.groups || [];
const TAG = {}; // tag → {group, label, owner}
for (const g of GROUPS) for (const tg of (g.tags || [])) TAG[tg.name] = { group: g.name, label: tg.label, owner: g.owner };
const groupTitle = g => g.owner === "ytif" ? (g.heading || g.name) : g.name;
// a filter row and a tag badge name the group the way a reader asks it
const groupLabel = g => g.owner === "ytif" ? (labelOf(g.label) || g.name) : g.name;
const groupSub = g => g.owner === "ytif" ? labelOf(g.label) : "";
const tagText = name => TAG[name] ? labelOf(TAG[name].label) : name;

const VERIFICATIONS = D.verifications || [];
const RUNNERS = [...new Set(VERIFICATIONS.map(c => c.runner).filter(Boolean))].sort();
const gatesOf = c => Array.isArray(c.gates) ? c.gates : [];
const BY_KEY = new Map(VERIFICATIONS.map((c, i) => [c.key, i]));
const componentOf = unit => String(unit || "").split("/").slice(0, 2).join("/") || ".";
const titleOf = c => c.impact || (c.is_command && Array.isArray(c.run) ? c.run.join(" ") : "") || c.name;
// shortName is the part of a verification's name that tells it apart in a
// list: a JS test's own title, a .NET test's method; the key keeps the rest.
function shortName(c) {
  const n = String(c.name || "");
  if (n.includes(" > ")) return n.slice(n.lastIndexOf(" > ") + 3);
  if (c.runner !== "dotnet-test") return n;
  const head = n.indexOf("(") < 0 ? n : n.slice(0, n.indexOf("("));
  return n.slice(head.lastIndexOf(".") + 1);
}
// breakable lets an identifier wrap only after its own separators
const breakable = (s, seps) => esc(s).replace(seps, "$&<wbr>");
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
const DERIVED = ["component", "unit"];
const INDEX = new Map(); // "facet:value" → ids in inventory order
const indexAdd = (k, id) => (INDEX.get(k) || INDEX.set(k, []).get(k)).push(id);
for (const c of VERIFICATIONS) {
  for (const tg of c.tags) indexAdd("tag:" + tg, c.id);
  for (const f of DERIVED) indexAdd(f + ":" + c.f[f][0], c.id);
}
const uses = tg => (INDEX.get("tag:" + tg) || []).length;

// each figure is a gate's mean over its window; the sum and the hits are what a row shows
const sumOf = r => GATES.reduce((s, g) => s + (r.means?.[g]?.ms || 0), 0);
const hitsOf = r => GATES.reduce((s, g) => s + (r.hits?.[g] || 0), 0);
// a sum of 0ms is a measurement; only a row with no sample has none
const sampled = r => GATES.some(g => r.means?.[g]);

// ledger files: the tree the page lists verifications by
const FILES = D.files || [];
const DEFAULT_FILE = "ytif-verifications.yaml";
const FILE_BY = new Map(FILES.map(f => [f.path, f]));
const FILE_KIDS = new Map();
const ROOT_FILE = (FILES.find(f => !f.parent) || {}).path || "";
for (const f of FILES) {
  if (!f.parent) continue;
  if (!FILE_KIDS.has(f.parent)) FILE_KIDS.set(f.parent, []);
  FILE_KIDS.get(f.parent).push(f.path);
}
// a label is the path below the including file's directory, without the default file name
function fileLabel(path, parent) {
  let rel = path;
  const dir = parent ? parent.split("/").slice(0, -1).join("/") : "";
  if (dir && rel.startsWith(dir + "/")) rel = rel.slice(dir.length + 1);
  if (rel !== DEFAULT_FILE && rel.endsWith("/" + DEFAULT_FILE)) rel = rel.slice(0, -(DEFAULT_FILE.length + 1));
  return rel;
}
const FILE_COUNT = new Map(); // path → verifications in it and its descendants
const FILE_SCOPE = new Map(); // path → its own path and every descendant's
function walkFiles(path) {
  const scope = [path];
  let count = VERIFICATIONS.filter(c => c.file === path).length;
  for (const kid of FILE_KIDS.get(path) || []) {
    walkFiles(kid);
    scope.push(...FILE_SCOPE.get(kid));
    count += FILE_COUNT.get(kid);
  }
  FILE_SCOPE.set(path, scope);
  FILE_COUNT.set(path, count);
}
if (ROOT_FILE) walkFiles(ROOT_FILE);
const filesOf = path => FILE_SCOPE.get(path) || FILES.map(f => f.path);

// process time: one row per runner, task, and unit
const PROCS = (D.processes || []).map((p, i) => ({ ...p, id: i }));
const isShared = p => p.what === "prepare" || p.what === "affected";
const PROC_NODES = (() => {
  const nodes = [{ kind: "all", id: "all", ids: PROCS.map(p => p.id) }];
  const shared = [], byRunner = new Map();
  for (const p of PROCS) {
    if (isShared(p)) { shared.push(p); continue }
    if (!byRunner.has(p.runner)) byRunner.set(p.runner, new Map());
    const units = byRunner.get(p.runner);
    if (!units.has(p.unit)) units.set(p.unit, []);
    units.get(p.unit).push(p);
  }
  const byText = (a, b) => String(a).localeCompare(String(b));
  for (const [runner, units] of [...byRunner].sort((a, b) => byText(a[0], b[0]))) {
    nodes.push({ kind: "runner", id: "runner:" + runner, runner, ids: [...units.values()].flat().map(p => p.id) });
    for (const [unit, ps] of [...units].sort((a, b) => byText(a[0], b[0])))
      nodes.push({ kind: "unit", id: "unit:" + runner + ":" + unit, runner, unit, ids: ps.map(p => p.id) });
  }
  if (shared.length) {
    nodes.push({ kind: "shared", ids: shared.map(p => p.id) });
    for (const p of shared) nodes.push({ kind: "proc", id: "proc:" + p.id, runner: p.runner, unit: p.unit, what: p.what, ids: [p.id] });
  }
  return nodes;
})();
const procNodeLabel = n => n.kind === "all" ? t("procAll") : n.kind === "shared" ? t("shared") : n.kind === "runner" ? n.runner : (n.unit || n.what);
const nodeOf = id => PROC_NODES.find(n => n.id === id) || PROC_NODES[0];

// selection context: recorded mode, fallback, revision, and omissions
const OMISSIONS = new Map(); // key → latest omission
for (const o of (D.omissions || [])) OMISSIONS.set(o.key, [o]);
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
// the expanded row groups its badges by tag group, the first harm tag in its own color
function tagRows(c) {
  const harm = (c.f.what || [])[0];
  return GROUPS.flatMap(g => {
    const names = c.f[g.name] || [];
    if (!names.length) return [];
    return [`<div class="tagrow"><span>${esc(groupLabel(g))}</span><span class="chips">${names.map(tg =>
      `<span class="chip${tg === harm ? " harm" : ""}${WARN.has(tg) ? " warn" : ""}"${tg === harm ? ` style="background:${hc(c)}"` : ""}>${esc(tagText(tg))}</span>`).join("")}</span></div>`];
  }).join("");
}
function gateBadges(c) {
  const gates = new Set(gatesOf(c));
  return `<span class="gates">${GATES.map(g =>
    `<span class="gbadge${gates.has(g) ? " on" : ""}" title="${esc(g)}">${esc(g)}</span>`).join("")}${c.skip_push ? `<span class="gbadge skip" title="skip-push">skip-push</span>` : ""}</span>`;
}

// ---- routing ----
function route() {
  const h = location.hash.replace(/^#/, "") || "/verifications";
  const [path, query] = h.split("?");
  const params = new URLSearchParams(query || "");
  if (path.startsWith("/verification/")) return { page: "diagram", key: decodeURIComponent(path.slice(14)), params };
  if (path.startsWith("/check/")) return { page: "diagram", key: decodeURIComponent(path.slice(7)), params };
  if (path === "/verification" || path === "/check") return { page: "diagram", key: null, params };
  if (path === "/vocabulary") return { page: "vocabulary", params };
  return { page: "verifications", params }; // #/checks and #/cost land here too
}
const setHash = h => { try { history.replaceState(null, "", h) } catch { location.hash = h } };

function header(r) {
  const tags = GROUPS.reduce((n, g) => n + (g.tags || []).length, 0);
  const last = store.get("sel", null);
  const tabs = [
    ["verifications", "#/verifications", t("tabVerifications"), VERIFICATIONS.length],
    ["diagram", last && BY_KEY.has(last) ? href(last) : "#/verification", t("tabDiagram"), null],
    ["vocabulary", "#/vocabulary", t("tabVocab"), tags],
  ];
  $("#tabs").innerHTML = tabs.map(([p, h, label, n]) =>
    `<a href="${h}"${r.page === p ? ` aria-current="page"` : ""}>${esc(label)}${n == null ? "" : ` <span class="count num">${fmt(n)}</span>`}</a>`).join("");
  $("#meta").textContent = t("meta", META.repo, META.version, when(META.generated));
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
// The page keeps its view in the URL: filters, grouping, the selected file, and
// the search write back through replaceState, so a reload or a shared link
// shows the same list. The expanded row is page state only.
const list = { q: "", shown: 50, filterOpen: false, fileFind: "", procFind: "", expanded: undefined };
const opened = new Set(); // expanded ledger files: the root and the selection's ancestors start open
let vp = null; // the state of the verifications page currently on screen: { st, f, draw }

// filterGroups describes every filter row: its key, its label, and one entry
// per chip with the value, the label, and the test.
function filterGroups() {
  return [
    { key: "hit", label: t("hitGroup"), opts: [["yes", t("hitYes"), c => hitsOf(c) > 0], ["no", t("hitNo"), c => hitsOf(c) === 0]] },
    { key: "gate", label: t("gates"), opts: GATES.map(g => [g, g, c => c.gates.includes(g)]) },
    { key: "runner", label: "runner", opts: RUNNERS.map(r => [r, r, c => c.runner === r]) },
    { key: "time", label: t("avgGroup"), opts: [["1s", t("atLeast", "1s"), c => sumOf(c) >= 1000], ["10s", t("atLeast", "10s"), c => sumOf(c) >= 10000], ["100s", t("atLeast", "100s"), c => sumOf(c) >= 100000]] },
    { key: "state", label: t("stateGroup"), opts: [["nosample", t("stateNoSample"), c => !Object.keys(c.means || {}).length], ["branch", t("stateBranch"), c => !!c.on_branch], ["fail", t("stateFail"), c => !!c.recent_fail]] },
    ...GROUPS.map(g => ({
      key: "tag:" + g.name, label: groupLabel(g), tag: true,
      opts: (g.tags || []).map(tg => [tg.name, labelOf(tg.label), c => c.f[g.name].includes(tg.name)]),
    })),
  ];
}
const blankFilters = () => Object.fromEntries(filterGroups().map(g => [g.key, []]));
const activeCount = f => Object.values(f).reduce((n, v) => n + v.length, 0);
// any chosen value matches within a group, except the average-sum and state
// groups, where every chosen condition must hold
const passGroup = (c, g, picked) => !picked.length ||
  (AND.has(g.key) ? g.opts.filter(o => picked.includes(o[0])).every(o => o[2](c)) : g.opts.filter(o => picked.includes(o[0])).some(o => o[2](c)));
const passAll = (c, groups, f, skip) => groups.every(g => g.key === skip || passGroup(c, g, f[g.key] || []));
const textMatch = (c, q) => !q || [c.name, c.accident, c.detection, c.impact, c.unit, (c.run || []).join(" ")].some(s => String(s || "").toLowerCase().includes(q));

function stateFrom(params) {
  const groups = filterGroups();
  const st = {
    view: params.get("view") === "process" ? "process" : "verifications",
    group: params.get("group") === "unit" ? "unit" : "verification",
    file: params.get("file") || ROOT_FILE,
    sel: nodeOf(params.get("runner") || "all").id,
    sort: store.get("sort", "avg"),
  };
  const f = blankFilters();
  for (const v of splitParam(params.get("tags"))) if (TAG[v]) f["tag:" + TAG[v].group].push(v);
  for (const v of splitParam(params.get("gates"))) if (GATES.includes(v)) f.gate.push(v);
  for (const k of ["hit", "time", "state"]) {
    const g = groups.find(x => x.key === k);
    for (const v of splitParam(params.get(k))) if (g.opts.some(o => o[0] === v)) f[k].push(v);
  }
  if (st.view !== "process") for (const v of splitParam(params.get("runner"))) if (RUNNERS.includes(v)) f.runner.push(v);
  return { st, f };
}

function syncHash(st, f) {
  const qs = new URLSearchParams();
  const tags = GROUPS.flatMap(g => f["tag:" + g.name]);
  if (tags.length) qs.set("tags", tags.join(","));
  if (f.gate.length) qs.set("gates", f.gate.join(","));
  for (const k of ["hit", "time", "state"]) if (f[k].length) qs.set(k, f[k].join(","));
  if (st.view === "process") { if (st.sel !== "all") qs.set("runner", st.sel) }
  else if (f.runner.length) qs.set("runner", f.runner.join(","));
  if (list.q) qs.set("q", list.q);
  if (st.file && st.file !== ROOT_FILE) qs.set("file", st.file);
  if (st.group === "unit") qs.set("group", "unit");
  if (st.view === "process") qs.set("view", "process");
  const s = qs.toString();
  setHash("#/verifications" + (s ? "?" + s : ""));
}

function storageBytes(n) {
  const units = ["B", "KiB", "MiB", "GiB"];
  let u = 0;
  while (n >= 1024 && u < units.length - 1) { n /= 1024; u++ }
  return `${fmt(Math.round(n * 10) / 10)} ${units[u]}`;
}

function retentionParts(pool, from) {
  const parts = [];
  if (pool?.boundary) parts.push(t("srcEvicted", day(pool.boundary.time), pool.boundary.id));
  else if (from && new Date(from) > new Date(new Date(META.generated).getTime() - CRITERIA.hits.days * 86400000)) parts.push(t("srcShort", CRITERIA.hits.days));
  if (pool?.legacy_bytes) parts.push(t("srcLegacy", storageBytes(pool.legacy_bytes)));
  if (pool?.overage) parts.push(t("srcOverage", storageBytes(pool.overage)));
  if (pool?.error) parts.push(t("srcMaintenance", pool.error));
  return parts;
}

function sourceLine() {
  const parts = [];
  const local = META.local || { attempts: {} };
  const runs = Object.values(local.attempts || {}).reduce((s, n) => s + n, 0);
  parts.push(local.from ? t("srcLocal", day(local.from), day(local.to), fmt(runs)) : t("srcLocalEmpty"));
  parts.push(...retentionParts(META.local_storage, local.from));
  const ci = META.ci;
  if (ci) {
    parts.push(t("srcCI", ci.repo, ci.from ? `${day(ci.from)} – ${day(ci.to)}` : "", ci.attempts ? fmt(ci.attempts) : ""));
    if (ci.status) {
      // a stopped sync says why; the range above still shows what the cache holds
      parts.push(t({ offline: "srcOffline", "no credentials": "srcNoCredentials", "no artifact": "srcNoArtifact" }[ci.status] || "srcOffline"));
    } else {
      if (ci.synced) parts.push(t("srcSynced", when(ci.synced)));
      if (ci.pending) parts.push(t("srcPending", fmt(ci.pending)));
    }
    parts.push(...retentionParts(ci.storage, ci.from));
  }
  return parts.join(" · ");
}

// gateSumHTML shows each gate's total twice: the sum of its members' means,
// and the timed results of its latest run on the current branch.
function gateSumHTML() {
  const avg = (D.summary || {}).average || [];
  const branch = (D.summary || {}).branch || [];
  const sum = rows => rows.reduce((s, r) => s + (r.ms || 0), 0);
  const avgTotal = sum(avg), branchTotal = sum(branch);
  const totalText = (rows, n) => rows.some(r => r.ms != null) ? ms(n) : "–";
  const bar = (rows, total) => `<span class="gbar">${total ? rows.map(r => r.ms ? `<span style="width:${(r.ms / total) * 100}%;background:${gateColor(r.gate)}"></span>` : "").join("") : ""}</span>`;
  const items = (rows, withTime) => rows.length ? `<div class="gitems">${rows.map(r =>
    `<span><i style="background:${gateColor(r.gate)}"></i><span class="note">${esc(r.gate)}</span><b>${r.ms == null ? "–" : ms(r.ms)}</b>${r.hits ? `<b>(${fmt(r.hits)})</b>` : ""}${withTime ? `<span class="note">${esc(when(r.time))}</span>` : ""}</span>`).join("")}</div>`
    : `<div class="gitems"><span class="note">${esc(t("noItem"))}</span></div>`;
  return `<section class="gatesum card">
    <div class="ghead"><b>${esc(t("gateTitle"))}</b><span class="glegend">${GATES.map(g => `<span><i style="background:${gateColor(g)}"></i>${esc(g)}</span>`).join("")}</span></div>
    <div class="grow">
      <span class="glabel">${esc(t("avg"))}</span>${bar(avg, avgTotal)}<b class="gtotal">${totalText(avg, avgTotal)}</b>
      <span class="gsp"></span>${items(avg, false)}<span class="gsp"></span>
      <span class="glabel">${esc(t("branch"))}<code>${esc(META.branch || t("noBranch"))}</code></span>${bar(branch, branchTotal)}<b class="gtotal">${totalText(branch, branchTotal)}</b>
      <span class="gsp"></span>${items(branch, true)}<span class="gsp"></span>
    </div>
  </section>`;
}

function criteriaHTML() {
  const ns = GATES.map(g => `${g} ${fmt(CRITERIA.average?.[g] ?? 0)}`).join(" · ");
  const to = META.generated ? day(META.generated) : "";
  const from = META.generated ? day(new Date(Date.parse(META.generated) - (CRITERIA.hits?.days || 365) * 864e5)) : "";
  const last = Object.entries(CRITERIA.hits?.last || {}).map(([g, n]) => t("criteriaHitLast", g, fmt(n)));
  return `<div><b>${esc(t("criteriaAvg"))}</b><span>${esc(t("criteriaAvgText"))}</span><span class="note">${esc(ns)}</span></div>
    <div><b>${esc(t("criteriaBranch"))}</b><span>${esc(t("criteriaBranchText"))}</span></div>
    <div><b>${esc(t("criteriaHit"))}</b><span>${esc(from)} – ${esc(to)}</span><span>${esc(t("criteriaHitText"))}</span><span class="note">${esc(t("criteriaHitCI"))}</span>${last.map(l => `<span class="note">${esc(l)}</span>`).join("")}</div>`;
}

const procNodePath = n => n.kind === "unit" ? `${n.runner} · ${n.unit}` : procNodeLabel(n);
const selPath = st => st.view === "process" ? procNodePath(nodeOf(st.sel)) : (st.file || ROOT_FILE || t("filesTitle"));
const sortOptions = view => {
  const opts = [["avg", t("sortAvg")]];
  if (view !== "process") opts.push(["inventory", t("sortInv")], ["hits", t("sortHits")]);
  for (const g of GATES) opts.push([g, t("sortGate", g)]);
  return opts;
};

function verificationsPage(r) {
  const { st, f } = stateFrom(r.params);
  list.q = r.params.get("q") || "";
  if (ROOT_FILE && !opened.size) {
    opened.add(ROOT_FILE);
    for (let p = st.file; p; p = FILE_BY.get(p)?.parent) opened.add(p);
  }
  $("#page").innerHTML = `<div class="pagehead"><h1>${esc(t("title"))}</h1><p class="note">${esc(sourceLine())}</p></div>
    ${gateSumHTML()}
    <nav class="views" aria-label="${esc(t("viewLabel"))}">${[["verifications", "viewVerif"], ["process", "viewProcess"]].map(([v, k]) =>
      `<button type="button" data-view="${v}" aria-pressed="${st.view === v}">${esc(t(k))}</button>`).join("")}</nav>
    <div class="listpage">
      <div class="sidecol">
        <aside class="files card" aria-label="${esc(st.view === "process" ? t("procFind") : t("filesTitle"))}">
          <div class="fhead"><b>${esc(st.view === "process" ? t("procTitle") : t("filesTitle"))}</b><span class="fcount">${esc(st.view === "process" ? fmt(PROCS.length) : t("files", fmt(FILES.length)))}</span></div>
          <label class="sr" for="sidefind">${esc(st.view === "process" ? t("procFind") : t("filesFind"))}</label>
          <input id="sidefind" type="search" placeholder="${esc(st.view === "process" ? t("procFind") : t("filesFind"))}" value="${esc(st.view === "process" ? list.procFind : list.fileFind)}">
          <div id="sidelist" role="tree"></div>
        </aside>
        <section class="criteria" aria-label="${esc(t("criteriaAvg"))}">${criteriaHTML()}</section>
      </div>
      <section class="listmain">
        <div class="toolbar">
          <span class="sel"><code id="selpath">${esc(selPath(st))}</code><span class="note" id="selcount"></span></span>
          <span class="tools2">
            ${st.view === "verifications" ? `<span class="seg" role="group" aria-label="${esc(t("grouping"))}">${[["verification", "groupVerification"], ["unit", "groupUnit"]].map(([v, k]) =>
              `<button type="button" data-group="${v}" aria-pressed="${st.group === v}">${esc(t(k))}</button>`).join("")}</span>` : ""}
            <label class="sr" for="sort">${esc(t("sort"))}</label>
            <select id="sort">${sortOptions(st.view).map(([v, label]) => `<option value="${v}"${st.sort === v ? " selected" : ""}>${esc(label)}</option>`).join("")}</select>
          </span>
        </div>
        ${st.view === "verifications" ? `<div class="card filtercard" role="group" aria-label="${esc(t("filters"))}">
          <div class="fbar">
            <label class="sr" for="q">${esc(t("searchList"))}</label>
            <input id="q" type="search" placeholder="${esc(t("searchList"))}" value="${esc(list.q)}">
            <button type="button" class="fbtn" id="ftoggle" aria-expanded="${list.filterOpen}">
              <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M2 4h12M4.5 8h7M7 12h2" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg>${esc(t("filters"))}<span class="badge" id="fbadge"${activeCount(f) ? "" : " hidden"}>${activeCount(f)}</span>
            </button>
          </div>
          <div class="fdetails" id="fdetails"${list.filterOpen ? "" : " hidden"}>
            <div class="fgrid" id="fgroups"></div>
            <p class="fnote" style="margin-left:104px">${esc(t("membershipNote"))}</p>
            <div class="fgrid tags" id="ftagroups"></div>
          </div>
          <div class="applied" id="applied"></div>
        </div>` : ""}
        <div class="card vtable" id="rows"></div>
      </section>
    </div>`;

  const draw = () => {
    const groups = filterGroups();
    if (st.view === "process") {
      const ids = new Set(nodeOf(st.sel).ids);
      // a process row names the task it ran, then the runner and unit it ran for
      const rows = PROCS.filter(p => ids.has(p.id)).map(p => ({ ...p, title: p.what, keyline: p.unit ? `${p.runner} · ${p.unit}` : p.runner }));
      $("#selcount").textContent = t("countProc", fmt(rows.length));
      drawProcSide(st);
      drawRows(st, sortRows(rows, st.sort));
      return;
    }
    const needle = list.q.trim().toLowerCase();
    const scope = filesOf(st.file);
    const inScope = VERIFICATIONS.filter(c => scope.includes(c.file));
    const base = inScope.filter(c => textMatch(c, needle));
    const kept = base.filter(c => passAll(c, groups, f));
    const count = activeCount(f);
    drawChips(groups, base, f);
    drawApplied(groups, f);
    const badge = $("#fbadge");
    badge.textContent = count;
    badge.hidden = !count;
    $("#selcount").textContent = (needle || count) ? t("countMatch", fmt(kept.length)) : t("countAll", fmt(inScope.length));
    drawSide(st);
    const rows = sortRows(st.group === "unit" ? unitRows(kept) : kept, st.sort);
    if (st.group === "verification" && list.expanded === undefined) list.expanded = rows.length ? rows[0].key : null;
    drawRows(st, rows);
  };
  vp = { st, f, draw };
  draw();
}

// sortRows orders by the chosen figure, keeping the listed order for ties.
function sortRows(rows, sort) {
  const key = {
    avg: r => sumOf(r),
    hits: r => hitsOf(r),
    inventory: r => typeof r.id === "number" ? -r.id : 0,
    check: r => r.means?.check?.ms ?? -1,
    commit: r => r.means?.commit?.ms ?? -1,
    push: r => r.means?.push?.ms ?? -1,
    ci: r => r.means?.ci?.ms ?? -1,
  }[sort] || (r => sumOf(r));
  return rows.map((r, i) => ({ r, i })).sort((a, b) => key(b.r) - key(a.r) || a.i - b.i).map(x => x.r);
}

// unitRows aggregates the listed verifications by their execution unit.
function unitRows(kept) {
  const by = new Map();
  for (const c of kept) {
    const k = c.unit || ".";
    if (!by.has(k)) by.set(k, []);
    by.get(k).push(c);
  }
  return [...by].map(([unit, cs]) => {
    const means = {}, hits = {}, gates = new Set();
    for (const g of GATES) {
      let sum = 0, n = 0, h = 0;
      for (const c of cs) {
        if (c.gates.includes(g)) gates.add(g);
        const m = c.means?.[g];
        if (m) { sum += m.ms; n += m.k }
        h += c.hits?.[g] || 0;
      }
      if (n) means[g] = { ms: sum, k: n };
      if (h) hits[g] = h;
    }
    const runners = [...new Set(cs.map(c => c.runner))].sort();
    return { unit, id: unit, title: unit, keyline: t("unitKey", runners.join(", "), fmt(cs.length)), means, hits, gates: [...gates] };
  });
}

const chev = open => `<span class="chev"><svg viewBox="0 0 12 12" aria-hidden="true" style="transform:rotate(${open ? 90 : 0}deg)"><path d="M4.5 2.5 L8 6 L4.5 9.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg></span>`;

// gateCell shows the gate's mean, or – when the row has no sample there or is
// not a member, followed by the gate's hits, which outlast membership.
function gateCell(r, g) {
  const m = r.means?.[g];
  const h = r.hits?.[g] || 0;
  return `<span class="vcell num${m ? "" : " none"}">${m ? ms(m.ms) : "–"}${h ? ` <b>(${fmt(h)})</b>` : ""}</span>`;
}

function detailHTML(c) {
  const rows = [["diagramAccident", c.accident], ["diagramImpact", c.impact || titleOf(c)], ["diagramDetection", c.detection], ["diagramDelete", c.delete_when]];
  return `<div class="vdetail">
    <div class="texts">${rows.map(([k, v]) => `<span${k === "diagramImpact" ? ` class="impact"` : ""}><b>${esc(t(k))}</b><span>${esc(v || "–")}</span></span>`).join("")}</div>
    <div class="side"><b>${esc(t("tags"))}</b>${tagRows(c) || `<span class="note">–</span>`}<a href="${href(c.key)}">${esc(t("openDiagram"))}</a></div>
    <div class="vloc"><b>${esc(t("location"))}</b><code>${breakable(c.key, /[:/.]/g)}</code></div>
  </div>`;
}

function drawRows(st, rows) {
  const box = $("#rows");
  if (!rows.length) {
    const text = st.view === "process" ? (PROCS.length ? t("noProcesses") : t("noRecords")) : (VERIFICATIONS.length ? t("noMatch") : t("noVerifications"));
    box.innerHTML = `<p class="empty">${esc(text)}</p>`;
    return;
  }
  const head = `<div class="vhead"><span></span><span>${esc(st.view === "process" ? t("colWhat") : st.group === "unit" ? t("colUnit") : t("colImpact"))} / ${esc(t("colAvg"))}</span>
    ${GATES.map(g => `<span class="r">${esc(g)}</span>`).join("")}</div>`;
  const shown = rows.slice(0, list.shown);
  const individual = st.view === "verifications" && st.group === "verification";
  // the bars use the diagram rail's scales: an execution unit sums its rows
  // an execution unit's ticks grow tenfold past the verification ticks to fit
  // the largest total; a process is a total; a verification uses its own ticks
  const scale = st.view === "process" ? SUM_TICKS
    : st.group === "unit" ? coverTicks(ROW_TICKS, Math.max(0, ...rows.map(sumOf))) : ROW_TICKS;
  const body = shown.map(r => {
    const title = individual ? titleOf(r) : r.title;
    const keyline = individual ? r.key : r.keyline;
    // a verification names its runner and its own name; the full key waits in the detail
    const second = individual
      ? `<span class="vname"><span class="runner">${esc(r.runner)}</span><code>${breakable(shortName(r), /[_/]/g)}</code></span>`
      : `<code>${esc(keyline)}</code>`;
    const cells = `<span class="what"><b>${esc(title)}</b>${second}${barHTML(timeBar([r], scale))}</span>${GATES.map(g => gateCell(r, g)).join("")}`;
    if (!individual) return `<div class="vrow plain"><span></span>${cells}</div>`;
    const open = list.expanded === r.key;
    return `<div class="vitem"><button type="button" class="vrow" data-open="${esc(r.key)}" title="${esc(r.key)}" aria-expanded="${open}">${chev(open)}${cells}</button>${open ? detailHTML(r) : ""}</div>`;
  }).join("");
  const rest = rows.length - shown.length;
  box.innerHTML = head + body + (rest > 0 ? `<div class="morebar"><button type="button" class="btn" id="more">${esc(t("more", fmt(Math.min(50, rest)), fmt(rest)))}</button></div>` : "");
}

function drawChips(groups, base, f) {
  const html = g => `<div class="frow"><span>${esc(g.label)}</span><span class="fchips">${g.opts.map(([v, label]) => {
    const on = f[g.key].includes(v);
    // a chip counts what stays with it chosen in place of its own group's picks
    const trial = { ...f, [g.key]: AND.has(g.key) ? (on ? f[g.key] : [...f[g.key], v]) : [v] };
    const n = base.filter(c => passAll(c, groups, trial)).length;
    return `<button type="button" class="fchip" data-chip-key="${esc(g.key)}" data-chip-value="${esc(v)}" aria-pressed="${on}"${!n && !on ? " disabled" : ""}>${esc(label)} <span class="n num">${fmt(n)}</span></button>`;
  }).join("")}</span></div>`;
  $("#fgroups").innerHTML = groups.filter(g => !g.tag).map(html).join("");
  $("#ftagroups").innerHTML = groups.filter(g => g.tag).map(html).join("");
}

function drawApplied(groups, f) {
  const rows = groups.flatMap(g => f[g.key].map(v => ({ key: g.key, value: v, label: g.label, text: g.opts.find(o => o[0] === v)?.[1] || v })));
  $("#applied").hidden = !rows.length;
  $("#applied").innerHTML = rows.length ? `<b>${esc(t("applied"))}</b>${rows.map(r =>
    `<button type="button" class="fchip" data-remove-key="${esc(r.key)}" data-remove-value="${esc(r.value)}" aria-label="${esc(t("remove", `${r.label} ${r.text}`))}"><span class="note">${esc(r.label)}</span>${esc(r.text)}<span class="x" aria-hidden="true">×</span></button>`).join("")}<button type="button" class="linkbtn" id="clr">${esc(t("clearAll"))}</button>` : "";
}

// drawSide lists the ledger files: a chevron expands, the label selects, and a
// selection lists only that file's and its descendants' verifications.
function drawSide(st) {
  const needle = list.fileFind.trim().toLowerCase();
  const memo = new Map();
  const match = p => {
    if (memo.has(p)) return memo.get(p);
    const f = FILE_BY.get(p);
    const yes = !needle || fileLabel(p, f.parent).toLowerCase().includes(needle) || p.toLowerCase().includes(needle) ||
      (FILE_KIDS.get(p) || []).some(match);
    memo.set(p, yes);
    return yes;
  };
  const out = [];
  const walk = (path, depth) => {
    if (!match(path)) return;
    const kids = FILE_KIDS.get(path) || [];
    const open = opened.has(path);
    out.push(`<div class="fnode" style="padding-left:${depth * 14}px">
      ${kids.length ? `<button type="button" class="fchev" data-chev="${esc(path)}" aria-expanded="${open}" aria-label="${esc(fileLabel(path, FILE_BY.get(path).parent))}"><svg viewBox="0 0 12 12" aria-hidden="true"><path d="M4.5 2.5 L8 6 L4.5 9.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg></button>` : `<span style="width:14px"></span>`}
      <button type="button" class="flabel" role="treeitem" data-file="${esc(path)}" aria-current="${path === st.file}" title="${esc(path)}">${esc(fileLabel(path, FILE_BY.get(path).parent))}</button>
      <span class="fcount" style="text-align:right">${fmt(FILE_COUNT.get(path) || 0)}</span>
    </div>`);
    if (needle || open) for (const kid of kids) walk(kid, depth + 1);
  };
  if (ROOT_FILE) walk(ROOT_FILE, 0);
  $("#sidelist").innerHTML = out.join("");
}

// drawProcSide lists runners and their units, with prepare and the nx lookup
// under Shared.
function drawProcSide(st) {
  const needle = list.procFind.trim().toLowerCase();
  const visible = n => !needle || String(procNodeLabel(n) || "").toLowerCase().includes(needle) || String(n.runner || "").toLowerCase().includes(needle);
  const out = [];
  let head = null;
  for (const n of PROC_NODES) {
    if (n.kind === "runner" || n.kind === "shared") { head = n; continue }
    if (!visible(n)) continue;
    if (head) {
      out.push(`<span class="fgroup-head">${esc(n.kind === "unit" ? head.runner : t("shared"))}</span>`);
      head = null;
    }
    out.push(`<div class="fnode"><span style="width:14px"></span><button type="button" class="flabel" role="treeitem" data-sel="${esc(n.id)}" aria-current="${n.id === st.sel}">${esc(procNodeLabel(n))}</button><span class="fcount" style="text-align:right">${fmt(n.ids.length)}</span></div>`);
  }
  $("#sidelist").innerHTML = out.join("");
}

// ---- verifications page events ----
$("#page").addEventListener("click", e => {
  if (!vp) return;
  const { st, f, draw } = vp;
  const view = e.target.closest("[data-view]");
  if (view) { st.view = view.dataset.view; syncHash(st, f); render(); return }
  const grp = e.target.closest("[data-group]");
  if (grp) { st.group = grp.dataset.group; list.shown = 50; syncHash(st, f); draw(); return }
  if (e.target.closest("#ftoggle")) {
    list.filterOpen = !list.filterOpen;
    $("#ftoggle").setAttribute("aria-expanded", String(list.filterOpen));
    $("#fdetails").hidden = !list.filterOpen;
    return;
  }
  if (e.target.closest("#clr")) { Object.assign(f, blankFilters()); list.shown = 50; syncHash(st, f); draw(); return }
  const rm = e.target.closest("[data-remove-key]");
  if (rm) {
    const k = rm.dataset.removeKey, v = rm.dataset.removeValue;
    f[k] = f[k].filter(x => x !== v);
    list.shown = 50; syncHash(st, f); draw(); return;
  }
  const chip = e.target.closest("[data-chip-key]");
  if (chip) {
    const k = chip.dataset.chipKey, v = chip.dataset.chipValue;
    f[k] = f[k].includes(v) ? f[k].filter(x => x !== v) : [...f[k], v];
    list.shown = 50; syncHash(st, f); draw(); return;
  }
  const file = e.target.closest("[data-file]");
  if (file) { st.file = file.dataset.file; list.shown = 50; syncHash(st, f); render(); return }
  const chevron = e.target.closest("[data-chev]");
  if (chevron) {
    const path = chevron.dataset.chev;
    if (opened.has(path)) opened.delete(path); else opened.add(path);
    drawSide(st);
    const again = [...document.querySelectorAll("[data-chev]")].find(b => b.dataset.chev === path);
    if (again) again.focus();
    return;
  }
  const sel = e.target.closest("[data-sel]");
  if (sel) { st.sel = sel.dataset.sel; syncHash(st, f); render(); return }
  const open = e.target.closest("[data-open]");
  if (open) { list.expanded = list.expanded === open.dataset.open ? null : open.dataset.open; draw(); return }
  if (e.target.closest("#more")) { list.shown += 50; draw() }
});
$("#page").addEventListener("input", e => {
  if (!vp) return;
  if (e.target.id === "q") { list.q = e.target.value; list.shown = 50; syncHash(vp.st, vp.f); vp.draw() }
  else if (e.target.id === "sidefind" && vp.st.view === "process") { list.procFind = e.target.value; drawProcSide(vp.st) }
  else if (e.target.id === "sidefind") { list.fileFind = e.target.value; drawSide(vp.st) }
});
$("#page").addEventListener("change", e => {
  if (!vp || e.target.id !== "sort") return;
  vp.st.sort = e.target.value;
  store.set("sort", vp.st.sort);
  vp.draw();
});

// ---- diagram rail ----
// The rail lists every verification, grouped by the ledger file that defines it
// or by the runner that executes it, and the diagram shows the row picked there.
// Its search, tag filter, folds, and collapsed state last for the session, so
// moving between verifications keeps the list where it was.
const RAIL_PAGE = 30;         // rows a unit group lists before "more"
const RAIL_FILTER_OPEN = 200; // a filter opens every group up to this many rows
const rail = { open: true, by: "file", q: "", tags: [], tagOpen: false, fold: {}, more: {} };
// the ledger file a verification is defined in: the record's file, or the
// origin line when it carries none
const fileOf = c => c.file || String(c.origin || "").split(": ")[0];
const shortUnit = u => String(u || ".").split("/").slice(-2).join("/");
// a rail unit is its file name when it is a file, else its last two directories
const railUnit = u => { const base = String(u || ".").split("/").pop(); return base.includes(".") ? base : shortUnit(u) };

// A row matches the search by the fields the list search reads, and the tag
// filter by any chosen tag within its group, with every chosen group taken.
function railTextHit(c, q) {
  return !q || [c.name, c.unit, c.file, c.impact, c.detection, c.accident, (c.run || []).join(" ")]
    .some(s => String(s || "").toLowerCase().includes(q));
}
const railTagHit = (c, except) => GROUPS.every(g => g.name === except ||
  !rail.tags.some(tg => TAG[tg] && TAG[tg].group === g.name) ||
  rail.tags.some(tg => TAG[tg] && TAG[tg].group === g.name && c.tags.includes(tg)));

// A time bar prints its ticks inside the track and its fill covers them; the
// value sits inside the filled end when it fits, and just past it otherwise,
// where it takes the first tick label's place. A tick label shows only while
// neither the fill nor the value reaches it. The ticks come from the ledger's
// board scales (ms): every interval between neighbors takes an equal width,
// and a value past the last tick fills the bar. A verification uses
// ROW_TICKS; group and process totals use SUM_TICKS.
const ROW_TICKS = CRITERIA.scales?.verification || [0, 100, 1000, 10000, 100000, 1000000];
const SUM_TICKS = CRITERIA.scales?.unit || [0, 10000, 100000, 1000000, 10000000];
// coverTicks extends ticks tenfold until the last one reaches max
const coverTicks = (ticks, max) => { const out = [...ticks]; while (out[out.length - 1] < max) out.push(out[out.length - 1] * 10); return out; };
function tickPos(v, ticks) {
  const n = ticks.length - 1;
  if (v <= ticks[0]) return 0;
  for (let i = 1; i <= n; i++) if (v <= ticks[i]) return (i - 1 + (v - ticks[i - 1]) / (ticks[i] - ticks[i - 1])) / n;
  return 1;
}
const scaleRight = (i, n) => i === n ? "3px" : `calc(${(((n - i) / n) * 100).toFixed(2)}% + 3px)`;
// the fill covers the scale, so the value takes the ink of the segment under it
const BAR_INK = { check: "var(--ink-check)", commit: "var(--ink-commit)", push: "var(--ink-push)", ci: "var(--ink-ci)" };
// a sampled 0ms is a time; only rows without samples read as empty
function timeBar(rows, ticks) {
  const byGate = gateMs(rows), total = GATES.reduce((s, g) => s + byGate[g], 0), has = rows.some(sampled);
  const len = has ? tickPos(total, ticks) : 0, n = ticks.length - 1;
  const used = GATES.filter(g => byGate[g] > 0), pct = (len * 100).toFixed(2) + "%", inside = len >= 0.17;
  return {
    segs: used.map(g => ({ color: gateColor(g), width: (len * byGate[g] / total * 100).toFixed(2) + "%" })),
    sum: has ? ms(total) : t("noTimeSamples"),
    left: inside ? "auto" : pct, right: inside ? `calc(100% - ${pct})` : "auto",
    ink: !has ? "var(--muted)" : inside ? BAR_INK[used[used.length - 1]] : "var(--fg)",
    step: (100 / n).toFixed(3) + "%",
    ticks: ticks.slice(1).map((v, i) => ({ label: ms(v), at: i + 1 }))
      .filter(k => has && k.at / n * 100 - 6 >= len * 100 + (inside ? 0 : 10))
      .map(k => ({ label: k.label, right: scaleRight(k.at, n) })),
  };
}
const gateMs = rows => Object.fromEntries(GATES.map(g => [g, rows.reduce((s, c) => s + (c.means?.[g]?.ms || 0), 0)]));
const barHTML = b => `<span class="tbar" style="--step:${b.step}">${b.ticks.map(k => `<span class="tick" style="right:${k.right}">${esc(k.label)}</span>`).join("")}<span class="segs">${b.segs.map(s => `<span style="width:${s.width};background:${s.color}"></span>`).join("")}</span><span class="value" style="left:${b.left};right:${b.right};color:${b.ink}">${esc(b.sum)}</span></span>`;
const railChev = `<span class="rchev" aria-hidden="true"><svg><use href="#i-chevron"/></svg></span>`;

// railTree draws the list and counts what it shows. A heading folds like a
// unit, and its sum bar shows folded or open.
function railTree(cur) {
  const q = rail.q.trim().toLowerCase(), filtering = q !== "" || rail.tags.length > 0;
  const outer = rail.by === "file" ? fileOf : c => c.runner || "";
  const tree = new Map();
  let shown = 0;
  for (const c of VERIFICATIONS) {
    if (!railTextHit(c, q) || !railTagHit(c)) continue;
    shown++;
    // a command's unit is only its working directory, so commands sit under the
    // heading without a unit group
    const a = outer(c), b = c.is_command ? "" : (c.unit || ".");
    if (!tree.has(a)) tree.set(a, new Map());
    const m = tree.get(a);
    if (!m.has(b)) m.set(b, []);
    m.get(b).push(c);
  }
  const out = [`<div class="rpad"></div>`];
  const row = (c, text, ind) => {
    const on = c === cur;
    return `<button type="button" role="treeitem" aria-current="${on}" title="${esc(c.key + " — " + titleOf(c))}" data-rail-go="${esc(c.key)}" class="rrow" style="padding-left:${ind}"><span class="rname"><span class="dot" style="background:${hc(c)}"></span><span class="mono">${esc(text)}</span></span>${barHTML(timeBar([c], ROW_TICKS))}</button>`;
  };
  const sep = (inset, weight, tone) => `<div class="rsep" aria-hidden="true" style="height:${weight};margin-left:${inset};background:${tone}"></div>`;
  for (const a of [...tree.keys()].sort()) {
    const m = tree.get(a), all = [...m.values()].flat(), hk = rail.by + "|" + a;
    const open = rail.fold[hk] ?? (all.includes(cur) || (filtering && shown <= RAIL_FILTER_OPEN));
    out.push(`<button type="button" aria-expanded="${open}" title="${esc(a)}" data-rail-fold="${esc(hk)}" class="rheadrow">${railChev}<span class="rheadtext">${esc(a)}</span><span class="rcount">${fmt(all.length)}</span></button><div class="rbar">${barHTML(timeBar(all, SUM_TICKS))}</div>`);
    if (!open) continue;
    for (const b of [...m.keys()].sort()) {
      const rows = m.get(b), gk = hk + "|" + b;
      if (b === "") { for (const c of rows) out.push(row(c, c.name + (c.unit && c.unit !== "." ? " · " + c.unit : ""), "22px")); continue }
      const isOpen = rail.fold[gk] ?? (rows.includes(cur) || (filtering && shown <= RAIL_FILTER_OPEN));
      out.push(`<button type="button" aria-expanded="${isOpen}" title="${esc(b)}" data-rail-fold="${esc(gk)}" class="rgroup">${railChev}<span class="runit">${esc(railUnit(b))}</span><span class="rcount">${fmt(rows.length)}</span></button><div class="rbar unit">${barHTML(timeBar(rows, SUM_TICKS))}</div>`);
      if (!isOpen) continue;
      const n = rail.more[gk] || RAIL_PAGE;
      for (const c of rows.slice(0, n)) out.push(row(c, shortName(c), "38px"));
      if (rows.length > n) out.push(`<button type="button" class="rmore" data-rail-more="${esc(gk)}">${esc(t("moreLeft", fmt(Math.min(RAIL_PAGE, rows.length - n)), fmt(rows.length - n)))}</button>`);
      // an opened unit ends with a rule indented to its rows, an opened file with one indented to its units
      out.push(sep("38px", "2px", "var(--line)"));
    }
    out.push(sep("22px", "3px", "var(--wire)"));
  }
  if (!shown) out.push(`<p class="rempty">${esc(t("railEmpty"))}</p>`);
  return { shown, html: out.join("") };
}

// railInner draws the whole rail: the count and its collapse button, the search
// with the tag filter beside it, the grouping switch, and the list.
function railInner(cur) {
  if (!rail.open) return `<div class="railclosed"><button type="button" class="iconbtn" data-rail-toggle aria-label="${esc(t("railOpen"))}" title="${esc(t("railOpen"))}"><svg class="i"><use href="#i-panel-open"/></svg></button></div>`;
  const { shown, html } = railTree(cur);
  const q = rail.q.trim().toLowerCase();
  // a chip counts what stays with it chosen in place of its own group's picks
  const chip = (tag, label, g) => {
    const on = rail.tags.includes(tag), n = VERIFICATIONS.filter(c => c.tags.includes(tag) && railTextHit(c, q) && railTagHit(c, g)).length;
    return `<button type="button" aria-pressed="${on}" data-rail-tag="${esc(tag)}"${n || on ? "" : ` data-empty="true"`}>${HARM[tag] ? `<i class="hdot" style="background:var(--h-${HARM[tag]})"></i>` : ""}${esc(label)}<span class="n num">${fmt(n)}</span></button>`;
  };
  const groups = GROUPS.filter(g => (g.tags || []).length).map(g =>
    `<div class="rgroup2"><span>${esc(groupLabel(g))}</span><div class="rchips">${(g.tags || []).map(tg => chip(tg.name, labelOf(tg.label) || tg.name, g.name)).join("")}</div></div>`).join("");
  return `<div class="rhead">
      <div class="rline"><b>${esc(shown === VERIFICATIONS.length ? t("countAll", fmt(VERIFICATIONS.length)) : t("railCount", fmt(VERIFICATIONS.length), fmt(shown)))}</b><button type="button" class="iconbtn" data-rail-toggle aria-label="${esc(t("railClose"))}" title="${esc(t("railClose"))}"><svg class="i"><use href="#i-panel-close"/></svg></button></div>
      <div class="rsearch"><label class="sr" for="railq">${esc(t("railSearch"))}</label><input id="railq" type="search" value="${esc(rail.q)}" placeholder="${esc(t("railSearch"))}"><span class="rfwrap"><button type="button" class="rfbtn" data-rail-filter aria-expanded="${rail.tagOpen}" data-on="${rail.tags.length > 0}" aria-controls="tagfilter" aria-label="${esc(t("railFilter"))}" title="${esc(t("railFilter"))}"><svg class="i"><use href="#i-filter"/></svg>${rail.tags.length ? `<span class="badge">${fmt(rail.tags.length)}</span>` : ""}</button><div class="rpanel" id="tagfilter" data-open="${rail.tagOpen}" aria-hidden="${!rail.tagOpen}"><div>${groups}${rail.tags.length ? `<button type="button" class="rclear" data-rail-tagclear>${esc(t("clearTags"))}</button>` : ""}</div></div></span></div>
      <div role="group" aria-label="${esc(t("railGroup"))}" class="rseg">${[["file", "railByFile"], ["runner", "railByRunner"]].map(([k, key]) => `<button type="button" aria-pressed="${rail.by === k}" data-rail-by="${k}">${esc(t(key))}</button>`).join("")}</div>
    </div>
    <div class="rtree" role="tree" aria-label="${esc(t("railLabel"))}" id="railtree">${html}</div>`;
}

// ---- diagram page ----
const PAGE = 5;
const mm = { open: {}, key: null, full: false, basis: false };
// the rail list's scroll a rail click carries into the next diagram
let railScroll = null;
function diagramPage(r) {
  vp = null;
  if (!VERIFICATIONS.length) { $("#page").innerHTML = `<p class="empty">${esc(t("noVerifications"))}</p>`; return }
  let key = r.key;
  if (key == null) { const last = store.get("sel", null); key = BY_KEY.has(last) ? last : VERIFICATIONS[0].key; setHash(href(key)) }
  const id = BY_KEY.get(key);
  if (id == null) { $("#page").innerHTML = `<p class="empty">${esc(t("noVerification"))} <code>${esc(key)}</code></p>`; return }
  if (mm.key !== key) { mm.open = {}; mm.key = key; mm.full = false; mm.basis = false }
  store.set("sel", key);
  const c = VERIFICATIONS[id];

  // the rail and the diagram share the whole width: the rail holds its own
  // scroll, the diagram takes the rest
  $("#page").innerHTML = `<div class="board" data-rail="${rail.open ? "open" : "closed"}">
      <aside class="rail" id="rail" aria-label="${esc(t("railLabel"))}">${railInner(c)}</aside>
      <div class="dg"><div class="mm" id="mm"><svg class="wires" id="wires" aria-hidden="true"></svg>
        <div class="side left" id="left"></div><article class="core" id="core"></article><div class="side right" id="right"></div></div></div>
    </div>`;
  // a rail click keeps the list where it was; any other arrival shows the selected row
  const tree = $("#railtree"), on = tree?.querySelector('[data-rail-go][aria-current="true"]');
  if (tree && railScroll != null) { tree.scrollTop = railScroll; on?.focus({ preventScroll: true }) }
  else on?.scrollIntoView({ block: "nearest" });
  railScroll = null;

  const core = $("#core");
  core.style.setProperty("--hc", hc(c));
  const omits = omissionsOf(c.key);
  const [head, field, text] = c.detection ? ["diagramDetection", "detection", c.detection]
    : c.is_command ? ["diagramCommand", "", (c.run || []).join(" ")]
    : ["diagramVerification", "", c.name];
  const states = [
    ...(c.on_branch ? [["on-branch", "stateBranch", "diagramBranchHint"]] : []),
    ...(c.recent_fail ? [["fail", "stateFail", "diagramFailHint"]] : []),
    ...(!c.gates.some(g => c.means?.[g]) ? [["", "stateNoSample", "diagramNoSampleHint"]] : []),
  ];
  // a contract head asks its question and names the ledger field on a second line
  const fieldHead = (label, raw) => `${esc(t(label))}${raw ? `<code>${esc(raw)}</code>` : ""}`;
  core.innerHTML = `<span class="eyebrow">${fieldHead(head, field)}</span>
    <p class="detection" id="detection">${esc(text)}</p>
    <button type="button" class="detection-toggle" id="detection-toggle" aria-expanded="${mm.full}" aria-controls="detection" hidden>${esc(t(mm.full ? "diagramCollapse" : "diagramShowAll"))}</button>
    ${c.is_command ? "" : `<div class="impact"><span class="impact-label">${fieldHead("diagramImpact", "impact")}</span><p>${esc(c.impact || "–")}</p></div>`}
    ${chips(c)}${gateBadges(c)}
    ${states.length ? `<div class="statepills">${states.map(([kind, label, tip]) => `<span class="statepill ${kind}" title="${esc(t(tip))}">${esc(t(label))}</span>`).join("")}</div>` : ""}` +
    // the headline is the name itself when there is nothing else to lead with
    (text === c.name ? "" : `<span class="mono note">${esc(c.name)}</span>`) +
    (omits.length ? `<span class="note">${esc(t("omittedReason", omits[omits.length - 1].reason || c.key))} · ${esc(omits[omits.length - 1].gate)} · ${esc(when(omits[omits.length - 1].time))}</span>` : "");
  const branch = (k, head, leaves) => `<div class="branch"><div class="bhead" data-wire="core" id="h-${k}">${head}</div><div class="leaves">${leaves.join("")}</div></div>`;
  const kv = (label, value) => `<div class="leaf kv" data-wire="h-loc"><b>${esc(label)}</b><span class="mono">${esc(value)}</span></div>`;
  const at = (c.origin || "").match(/:\s*(verifications\[\d+\])$/)?.[1];
  const locLeaves = [
    kv("key", c.key), kv("runner", c.runner), kv(t("diagramUnit"), c.unit),
    ...(c.is_command ? [kv(t("run"), (c.run || []).join(" "))] : []),
    ...(c.cwd ? [kv(t("cwd"), c.cwd)] : []),
    ...(c.exclusive?.length ? [kv(t("exclusive"), c.exclusive.join(", "))] : []),
    ...(c.file ? [`<div class="leaf kv" data-wire="h-loc"><b>${esc(t("origin"))}</b><span class="mono"><a href="#/verifications?file=${encodeURIComponent(c.file)}" title="${esc(t("diagramFileHint"))}">${esc(c.file)}</a>${at ? ` · ${esc(at)}` : ""}</span><span class="note">${esc(t("diagramFileHint"))}</span></div>`]
      : c.origin ? [kv(t("origin"), c.origin)] : []),
  ];
  const ci = META.ci;
  const sync = ci ? "CI · " + (ci.status
    ? t({ offline: "srcOffline", "no credentials": "srcNoCredentials", "no artifact": "srcNoArtifact" }[ci.status] || "srcOffline")
    : ci.synced ? t("srcSynced", when(ci.synced)) : t("diagramUnsynced")) : t("diagramLocal");
  const processID = `unit:${c.runner}:${c.unit}`;
  const processNode = PROC_NODES.find(n => n.id === processID) || PROC_NODES.find(n => n.id === "runner:" + c.runner) || PROC_NODES[0];
  const costLeaves = [
    `<div class="leaf costbar" data-wire="h-cost"><b>${esc(t("colAvg"))}</b>${barHTML(timeBar([c], ROW_TICKS))}</div>`,
    `<div class="leaf costgates" data-wire="h-cost">${GATES.map(g => {
      const m = c.means?.[g];
      const tip = m ? t("diagramSamples", fmt(m.k)) : t(c.gates.includes(g) ? "diagramNoSample" : "diagramNotMember");
      return `<div class="costgate" title="${esc(tip)}"><i style="background:${gateColor(g)}"></i><code>${g}</code>${gateCell(c, g)}</div>`;
    }).join("")}<p class="note">${esc(t("diagramCostHint"))}</p></div>`,
    `<details class="leaf record-basis"${mm.basis ? " open" : ""} data-wire="h-cost"><summary><b>${esc(t("diagramBasis"))}</b> <span class="sync-status${ci?.status && ci.status !== "no artifact" ? " error" : ""}">· ${esc(sync)}</span></summary><div class="criteria">${criteriaHTML()}</div><p class="note source">${esc(sourceLine())}</p></details>`,
    `<a class="leaf process-link" data-wire="h-cost" href="#/verifications?view=process&runner=${encodeURIComponent(processNode.id)}">${esc(t("diagramProcess"))}</a>`,
  ];
  const textLeaves = (k, v) => v ? [`<div class="leaf text" data-wire="h-${k}">${esc(v)}</div>`] : [`<div class="leaf" data-wire="h-${k}"><span class="note">–</span></div>`];
  $("#left").innerHTML = [
    ...(c.is_command ? [] : [
      branch("accident", fieldHead("diagramAccident", "accident"), textLeaves("accident", c.accident)),
      branch("delete", fieldHead("diagramDelete", "delete_when"), textLeaves("delete", c.delete_when)),
    ]),
    branch("cost", esc(t("diagramCost")), costLeaves),
    branch("loc", esc(t("location")), locLeaves),
  ].join("");

  const peers = (k, ids, parent) => {
    const n = mm.open[k] || 0, w = `data-wire="${parent}"`;
    if (!ids.length) return [`<div class="leaf" ${w}><span class="note">${esc(t("noPeers"))}</span></div>`];
    const toggle = `<button type="button" class="toggle" ${w} data-toggle="${esc(k)}" aria-expanded="${n > 0}">${esc(n > 0 ? t("hide") : t("show", fmt(ids.length)))}</button>`;
    if (!n) return [toggle];
    return [toggle, ...ids.slice(0, n).map(id => peer(VERIFICATIONS[id], w)),
      ...(n < ids.length ? [`<button type="button" class="more" data-more="${esc(k)}">${esc(t("moreLeft", fmt(Math.min(PAGE, ids.length - n)), fmt(ids.length - n)))}</button>`] : [])];
  };
  const peer = (o, w) => `<a class="peer" ${w} href="${href(o.key)}" title="${esc(o.name)}"><span class="dot" style="background:${hc(o)}"></span><span><span class="txt">${esc(titleOf(o))}</span><small class="peer-meta">${esc(t("diagramPeerMeta", sampled(o) ? ms(sumOf(o)) : "–", fmt(hitsOf(o))))}</small></span></a>`;
  const sub = (k, text, raw, pid, parent) => {
    const tag = k.startsWith("tag:");
    return `<div class="sub"><${tag ? "a" : "span"} class="tagpill" id="${pid}" data-wire="${parent}" title="${esc(tag ? t("diagramTagHint") : raw)}"${tag ? ` href="#/verifications?tags=${encodeURIComponent(raw)}"` : ""}>${esc(text)}${raw !== text ? `<code>${esc(raw)}</code>` : ""}</${tag ? "a" : "span"}><div class="leaves">${peers(k, (INDEX.get(k) || []).filter(id => id !== c.id), pid).join("")}</div></div>`;
  };

  const rels = [
    ["requires", "requires", "diagramRequiresHint", (c.requires || []).map(k => BY_KEY.get(k)).filter(i => i != null)],
    ["requiredBy", "← requires", "diagramRequiredByHint", REQUIRED_BY.get(c.key) || []],
    ["ensures", "ensures", "diagramEnsuresHint", (c.ensures || []).map(k => BY_KEY.get(k)).filter(i => i != null)],
    ["ensuredBy", "← ensures", "diagramEnsuredByHint", ENSURED_BY.get(c.key) || []],
  ].filter(([, , , ids]) => ids.length);
  // a relation pill names what it reads and the ledger kind on its own line
  const relBranch = rels.length ? [branch("rel", esc(t("relations")), rels.map(([k, raw, tip, ids], i) =>
    `<div class="sub relation"><span class="tagpill" id="t-rel-${i}" data-wire="h-rel" title="${esc(t(tip))}">${esc(t(k))}<code>${esc(raw)}</code></span><div class="leaves">${ids.map(x => peer(VERIFICATIONS[x], `data-wire="t-rel-${i}"`)).join("")}</div></div>`))] : [];
  const tags = knownTags(c);
  $("#right").innerHTML = [
    ...relBranch,
    branch("tags", `${esc(t("tags"))}<span class="bnote">${fmt(tags.length)}</span>`, tags.map((tg, i) => sub("tag:" + tg, tagText(tg), tg, `t-tag-${i}`, "h-tags"))),
    ...DERIVED.map(f => branch(f, esc(t(f)), [sub(f + ":" + c.f[f][0], c.f[f][0], c.f[f][0], `t-${f}`, `h-${f}`)])),
  ].join("");

  // the rail keeps its own state; a redraw keeps the rest of the page as it is
  const board = $(".board");
  // the tag panel closes when the pointer leaves the button and the panel
  const watchRail = () => { const w = $(".rfwrap"); if (w) w.onmouseleave = () => { if (rail.tagOpen) { rail.tagOpen = false; drawRail() } } };
  const drawRail = () => { $("#rail").innerHTML = railInner(c); watchRail() };
  const drawRailTree = () => { const el = $("#railtree"); if (el) el.innerHTML = railTree(c).html };
  $("#rail").addEventListener("click", e => {
    const b = e.target.closest("[data-rail-toggle],[data-rail-by],[data-rail-fold],[data-rail-more],[data-rail-go],[data-rail-tag],[data-rail-tagclear],[data-rail-filter]");
    if (!b) return;
    const d = b.dataset;
    if ("railToggle" in d) { rail.open = !rail.open; board.dataset.rail = rail.open ? "open" : "closed"; drawRail(); scheduleWires(); return }
    if (d.railBy) { rail.by = d.railBy; rail.fold = {}; rail.more = {}; drawRail(); return }
    if (d.railGo) { if (b.getAttribute("aria-current") !== "true") { railScroll = $("#railtree").scrollTop; location.hash = href(d.railGo) } return }
    // a fold remembers the state it left, not the state it would default to
    if (d.railFold) { rail.fold[d.railFold] = b.getAttribute("aria-expanded") !== "true"; drawRailTree(); return }
    if (d.railMore) { rail.more[d.railMore] = (rail.more[d.railMore] || RAIL_PAGE) + RAIL_PAGE; drawRailTree(); return }
    if (d.railTag) {
      rail.tags = rail.tags.includes(d.railTag) ? rail.tags.filter(x => x !== d.railTag) : [...rail.tags, d.railTag];
      rail.fold = {}; rail.more = {}; drawRail();
      const again = [...$("#rail").querySelectorAll("[data-rail-tag]")].find(x => x.dataset.railTag === d.railTag);
      if (again) again.focus();
      return;
    }
    if ("railTagclear" in d) { rail.tags = []; rail.fold = {}; rail.more = {}; drawRail(); return }
    if ("railFilter" in d) { rail.tagOpen = !rail.tagOpen; drawRail(); return }
  });
  $("#rail").addEventListener("input", e => {
    if (e.target.id !== "railq") return;
    rail.q = e.target.value; rail.fold = {}; rail.more = {};
    drawRailTree();
  });
  $("#rail").addEventListener("keydown", e => {
    if (e.key === "Escape" && rail.tagOpen) { rail.tagOpen = false; drawRail(); $(".rfbtn")?.focus() }
  });
  watchRail();

  $("#mm").addEventListener("click", e => {
    const tg = e.target.closest("[data-toggle]"), m = e.target.closest("[data-more]");
    if (e.target.closest("#detection-toggle")) {
      mm.full = !mm.full;
      render();
      $("#detection-toggle")?.focus({ preventScroll: true });
    } else if (tg) { const k = tg.dataset.toggle; mm.open[k] = mm.open[k] ? 0 : PAGE; render() }
    else if (m) { const k = m.dataset.more; mm.open[k] = (mm.open[k] || 0) + PAGE; render() }
  });
  $(".record-basis").addEventListener("toggle", e => { mm.basis = e.target.open; scheduleWires() });
  syncClamp();
  for (const el of [$("#rail"), $("#mm"), core]) diagramResize.observe(el);
  syncStick();
  requestAnimationFrame(drawWires);
}

// the detection is clamped to seven lines, and the toggle appears only when the
// text really runs past them
function syncClamp() {
  const p = $("#detection"), b = $("#detection-toggle");
  if (!p || !b) return;
  const line = parseFloat(getComputedStyle(p).lineHeight) || 24;
  p.classList.remove("clamped");
  const over = p.scrollHeight > line * 7 + 1;
  b.hidden = !over;
  b.textContent = t(mm.full ? "diagramCollapse" : "diagramShowAll");
  b.setAttribute("aria-expanded", String(mm.full));
  if (over && !mm.full) p.classList.add("clamped");
}

document.addEventListener("mousedown", e => {
  if (!$("#langmenu").hidden && !e.target.closest(".menuwrap")) langMenu.close(false);
});

function drawWires() {
  const wrap = $("#mm"), svg = $("#wires");
  if (!wrap || getComputedStyle(svg).display === "none") return;
  const box = wrap.getBoundingClientRect(), core = $("#core").getBoundingClientRect(), paths = [];
  wrap.querySelectorAll("[data-wire]").forEach(el => {
    const from = document.getElementById(el.dataset.wire); if (!from) return;
    const a = from.getBoundingClientRect(), b = el.getBoundingClientRect();
    if (b.top >= a.bottom - 1 && b.left < a.right && b.right > a.left) {
      // stacked: a trunk in the indent gutter on the side facing the core, then an elbow into the child
      const inward = core.left > a.left ? -1 : 1;
      const x = (inward < 0 ? a.right : a.left) + inward * 9 - box.left, xe = (inward < 0 ? b.right : b.left) - box.left;
      const y1 = a.bottom - box.top, y2 = b.top + Math.min(b.height / 2, 18) - box.top;
      paths.push(`<path d="M${x},${y1} V${y2 - 6} Q${x},${y2} ${x + inward * 6},${y2} H${xe}"/>`);
      return;
    }
    const toRight = b.left >= a.right - 1;
    const x1 = (toRight ? a.right : a.left) - box.left, y1 = a.top + a.height / 2 - box.top;
    const x2 = (toRight ? b.left : b.right) - box.left, y2 = b.top + Math.min(b.height / 2, 18) - box.top;
    const dx = (x2 - x1) * 0.5;
    paths.push(`<path class="${from.id === "core" ? "core-wire" : ""}" d="M${x1},${y1} C${x1 + dx},${y1} ${x2 - dx},${y2} ${x2},${y2}"/>`);
  });
  svg.innerHTML = paths.join("");
}
// the core follows the page while it fits; a card taller than the viewport
// scrolls with it instead
function syncStick() {
  const c = $("#core");
  if (c) c.classList.toggle("tall", c.getBoundingClientRect().height > window.innerHeight - 24);
}
let wireFrame = 0;
const scheduleWires = () => { if (!wireFrame) wireFrame = requestAnimationFrame(() => { wireFrame = 0; syncStick(); drawWires() }) };
const diagramResize = new ResizeObserver(scheduleWires);
document.fonts.ready.then(scheduleWires);
window.addEventListener("resize", () => { syncClamp(); scheduleWires() });
window.addEventListener("scroll", scheduleWires, { passive: true });

// ---- vocabulary page ----
function vocabularyPage() {
  vp = null;
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
  diagramResize.disconnect();
  const r = route();
  // the diagram page runs edge to edge: its rail holds the left column
  $("#page").className = r.page === "diagram" ? "diagram" : "";
  header(r);
  ({ verifications: verificationsPage, diagram: diagramPage, vocabulary: vocabularyPage })[r.page](r);
}
window.addEventListener("hashchange", () => { render(); if (route().page === "diagram") window.scrollTo(0, 0) });
render();
