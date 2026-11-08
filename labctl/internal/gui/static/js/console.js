// GUI de labctl — console : redimensionnement, ligne de commande labctl.
// Module ES : ce qu'il partage est exporté, ce qu'il utilise est importé.
import { $, api, ask, esc, toast } from "./util.js";
import { running } from "./core.js";
import { run } from "./run.js";

// --- Console : panneau redimensionnable (hauteur mémorisée dans ce navigateur), et ligne de commande labctl
// avec la complétion du terminal (cobra, via /api/complete). Seules les commandes labctl : le serveur refuse le
// reste, et les commandes destructives demandent confirmation (elles tournent ensuite sans autre question).
const LOGH_MIN = 120, LOGH_DEFAULT = 240;
function setLogH(h, save = true) {
  h = Math.round(Math.max(LOGH_MIN, Math.min(h, innerHeight - 80)));
  document.documentElement.style.setProperty("--logh", h + "px");
  if (save) try { localStorage.setItem("console-h", String(h)); } catch {}
}
try { const h = Number(localStorage.getItem("console-h")); if (h) setLogH(h, false); } catch {}
$("#log-grip").addEventListener("pointerdown", e => {
  e.preventDefault();
  const grip = e.currentTarget;
  grip.setPointerCapture(e.pointerId);
  const move = ev => setLogH(innerHeight - ev.clientY);
  const up = () => { grip.removeEventListener("pointermove", move); grip.removeEventListener("pointerup", up); };
  grip.addEventListener("pointermove", move); grip.addEventListener("pointerup", up);
});
$("#log-grip").addEventListener("dblclick", () => setLogH($("#log").offsetHeight < innerHeight * 0.6 ? innerHeight - 80 : LOGH_DEFAULT));

const cin = $("#console-in"), sugg = $("#console-sugg");
let hist = [], hpos = -1, sitems = [], ssel = -1;
try { hist = JSON.parse(localStorage.getItem("console-hist") || "[]"); } catch {}

// splitArgs découpe comme un shell simple : espaces, '…' et "…" (pour « netlab ssh poste 'ip a' »).
function splitArgs(s) {
  const out = []; let cur = "", q = "", any = false;
  for (const c of s) {
    if (q) { if (c === q) q = ""; else cur += c; }
    else if (c === "'" || c === '"') { q = c; any = true; }
    else if (/\s/.test(c)) { if (cur || any) out.push(cur); cur = ""; any = false; }
    else cur += c;
  }
  if (cur || any) out.push(cur);
  return { args: out, open: !!q };
}
const atWordStart = v => !v.trim() || /\s$/.test(v);
function hideSugg() { sugg.hidden = true; sitems = []; ssel = -1; }
function showSugg() {
  sugg.innerHTML = sitems.slice(0, 60).map((it, i) =>
    `<div class="${i === ssel ? "sel" : ""}" data-sugg="${i}"><span>${esc(it.v)}</span><span class="d">${esc(it.d || "")}</span></div>`).join("");
  sugg.hidden = false;
  sugg.querySelector(".sel")?.scrollIntoView({ block: "nearest" });
}
// acceptWord remplace le mot en cours par `word` (suivi d'une espace, sauf directive cobra « pas d'espace »).
function acceptWord(word, space = true) {
  const v = cin.value, base = atWordStart(v) ? v : v.slice(0, v.length - (v.match(/\S+$/) || [""])[0].length);
  cin.value = base + word + (space ? " " : "");
  hideSugg(); cin.focus();
}
async function completeLine() {
  const v = cin.value, { args } = splitArgs(v);
  if (args[0] === "labctl") args.shift();
  const cur = atWordStart(v) ? "" : (args.pop() ?? "");
  let r;
  try { r = await (await api("/api/complete", { method: "POST", body: JSON.stringify({ args: [...args, cur] }) })).json(); }
  catch (e) { toast(e.message, true); return; }
  if (cin.value !== v) return; // ligne modifiée entre-temps
  const items = r.items || [], space = !(r.directive & 2);
  if (!items.length) { hideSugg(); return; }
  if (items.length === 1) { acceptWord(items[0].v, space); return; }
  let p = items[0].v; // complète le préfixe commun, puis montre les choix
  for (const it of items) while (!it.v.startsWith(p)) p = p.slice(0, -1);
  if (p.length > cur.length) acceptWord(p, false);
  sitems = items; ssel = -1; showSugg();
}
cin.addEventListener("keydown", e => {
  if (e.key === "Tab") {
    e.preventDefault();
    if (!sugg.hidden && ssel >= 0) acceptWord(sitems[ssel].v); else completeLine();
  } else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
    e.preventDefault();
    const d = e.key === "ArrowDown" ? 1 : -1;
    if (!sugg.hidden) { ssel = (ssel + d + sitems.length) % sitems.length; showSugg(); return; }
    hpos = Math.max(-1, Math.min(hist.length - 1, hpos - d)); // ↑ remonte l'historique
    cin.value = hpos < 0 ? "" : hist[hpos];
  } else if (e.key === "Escape") hideSugg();
  else if (e.key === "Enter" && !sugg.hidden && ssel >= 0) { e.preventDefault(); acceptWord(sitems[ssel].v); }
});
cin.addEventListener("input", hideSugg);
sugg.addEventListener("mousedown", e => {
  const d = e.target.closest("[data-sugg]");
  if (d) { e.preventDefault(); acceptWord(sitems[Number(d.dataset.sugg)].v); }
});
$("#console-form").addEventListener("submit", async e => {
  e.preventDefault();
  const line = cin.value.trim();
  const { args, open } = splitArgs(line);
  if (args[0] === "labctl") args.shift();
  if (!args.length) return;
  if (open) { toast("guillemet non fermé", true); return; }
  if (running) { toast("une action est déjà en cours", true); return; }
  // Confirmation avant ce qui détruit, revient en arrière, ou ouvre un accès réseau (Internet, lien vers un autre
  // module) : une seule ligne ne doit pas exposer une cible vulnérable sans question.
  const destroys = args.find(a => ["destroy", "delete", "del", "release", "renew"].includes(a));
  const why = destroys ? "Commande destructive."
    : args.includes("restore") ? "Retour à un snapshot : les changements depuis sont perdus."
    : (args.includes("internet") && args.includes("on")) || args.includes("link")
      ? "Ouvre un accès réseau (Internet ou un autre module) : à éviter vers une cible vulnérable." : "";
  if (why && !(await ask({ title: "Confirmer", danger: !!destroys || why.startsWith("Ouvre"), ok: "Lancer",
      text: `labctl ${args.join(" ")}\n\n${why} Elle s'exécutera sans autre question.` }))) return;
  hist = [line, ...hist.filter(h => h !== line)].slice(0, 100); hpos = -1;
  try { localStorage.setItem("console-hist", JSON.stringify(hist)); } catch {}
  cin.value = ""; hideSugg();
  await run(args);
  cin.focus();
});
