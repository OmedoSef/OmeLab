// GUI de labctl — état partagé, navigation, chargement des données.
// Module ES : ce qu'il partage est exporté, ce qu'il utilise est importé.
import { $, api, ask, esc, lastOctet, toast, token } from "./util.js";
import { render } from "./render.js";
import { renderPending } from "./pending.js";
import { renderNet } from "./net.js";
import { loadFiles } from "./files.js";
import { loadTraffic } from "./traffic.js";
import { renderCore } from "./gateway.js";
import { renderPKI } from "./pki.js";
import { ed, openEditor } from "./editor.js";

// Nouvelle adresse (labctl gui relancé) collée dans un onglet déjà ouvert : seul le fragment change,
// le navigateur ne recharge pas ; on recharge pour prendre le nouveau jeton. Sinon, c'est une navigation (précédent…)
addEventListener("hashchange", () => {
  const h = new URLSearchParams(location.hash.slice(1));
  if ((h.get("t") || "") !== token) location.reload(); else if (h.get("p") !== page) go(h.get("p"));
});

export let state = null, running = false, current = null, core = null, pki = null, pkiTrust = null, trustChecking = false, config = null;

// Page affichée : overview, core, pki, new (brouillon de nouveau module), m/<module>/<vms|yaml|files|net>.
// Elle est dans l'adresse (#t=…&p=…) : un rechargement ou le bouton précédent y ramènent.
export let page = "overview";

export const modPage = () => page.startsWith("m/") ? page.split("/").slice(1) : [page === "new" ? ed.name : "", page === "new" ? "yaml" : ""];

export const openCmd = new Set(); // champs « autre commande » dépliés (le rendu toutes les 5 s ne doit pas les refermer)

document.addEventListener("toggle", e => {
  const d = e.target.closest?.("details[data-cmd-box]");
  if (d) d.open ? openCmd.add(d.dataset.cmdBox) : openCmd.delete(d.dataset.cmdBox);
}, true);

export function go(p) {
  p = p || "overview";
  const [kind, name, sub] = p.split("/");
  if (kind === "m") p = name ? `m/${name}/${["vms", "yaml", "files", "net"].includes(sub) ? sub : "vms"}` : "overview";
  else if (!["overview", "core", "pki", "traffic", "new"].includes(p) || (p === "new" && !ed.create)) p = "overview";
  const changed = p !== page;
  page = p;
  if (location.hash !== `#t=${token}&p=${p}`) history[changed ? "pushState" : "replaceState"](null, "", `#t=${token}&p=${p}`);
  try { localStorage.setItem("labs-page", p); } catch {}
  const [m, s] = modPage();
  $("#view-overview").hidden = p !== "overview";
  $("#view-core").hidden = p !== "core";
  $("#view-pki").hidden = p !== "pki";
  $("#view-traffic").hidden = p !== "traffic";
  $("#view-module").hidden = !m;
  $("#mod-vms").hidden = s !== "vms";
  $("#view-editor").hidden = s !== "yaml";
  $("#mod-net").hidden = s !== "net";
  $("#mod-files").hidden = s !== "files";
  render(); renderNet();
  if (changed) scrollTo(0, 0);
  if (p === "core") loadCore(); else if (p === "pki") loadPKI(); else if (p === "traffic") loadTraffic();
  else if (s === "yaml" && p !== "new") { ed.want = m; openEditor(); }
  else if (s === "net") loadConfig();
  else if (s === "files") loadFiles(m);
  else if (s === "vms" && m && !pki) loadPKI().then(render); // état de la PKI (fichiers locaux) pour le raccourci confiance
}

export async function loadConfig() {
  try { config = await (await api("/api/config")).json(); renderPending(); renderNet(); render(); }
  catch (e) { $("#pending").innerHTML = `<div class="banner"><span class="err">${esc(e.message)}</span></div>`; }
}

export async function editConfig(body) {
  try { await api("/api/config", { method: "POST", body: JSON.stringify(body) }); await loadConfig(); return true; }
  catch (e) { toast(e.message, true); return false; }
}

// Débrancher une VM externe (câblage local) : retire la carte et l'entrée de network.local.yaml.
export async function detachVM({ module, vm, name }) {
  if (!await ask({ title: "Débrancher", text: `Retirer ${vm} du réseau de ${module} ?`, ok: "Débrancher", danger: true })) return;
  if (await editConfig({ op: "attach-del", module, target: vm })) { toast(`${name} débranchée de ${module}`); load(true); }
}

// Brancher une VM d'un autre module : choisir la VM (à IP fixe), puis une adresse libre sur ce réseau.
export async function attachPicker(on) {
  const host = (state.modules || []).find(m => m.name === on);
  if (!host) return;
  const already = new Set((host.attached || []).map(a => a.from));
  const cands = [];
  for (const m of state.modules || []) {
    if (m.name === on) continue;
    for (const v of m.vms || []) {
      const ref = `${m.name}/${v.name}`;
      if (/\.\d+$/.test(v.ip || "") && !already.has(ref)) cands.push(ref);
    }
  }
  if (!cands.length) { toast("Aucune VM à IP fixe à brancher (les autres modules n'en déclarent pas, ou elles sont déjà branchées)"); return; }
  const ref = await ask({ title: `Brancher sur ${on}`, text: "Choisir la VM à brancher sur ce réseau :", options: cands, ok: "Suivant" });
  if (!ref) return;
  const used = new Set();
  for (const v of host.vms || []) used.add(lastOctet(v.ip));
  for (const a of host.attached || []) used.add(lastOctet(a.ip));
  let free = 50; while (free <= 99 && used.has(free)) free++;
  const s = await ask({ title: `Adresse de ${ref}`, text: `Dernier octet (2 à 99) sur ${host.subnet}.x :`, input: true, value: String(free), pattern: "[0-9]{1,3}", hint: "un nombre entre 2 et 99", ok: "Brancher" });
  if (!s) return;
  const octet = parseInt(s, 10);
  if (!(octet >= 2 && octet <= 99)) { toast("Adresse hors plage (2 à 99)", true); return; }
  if (await editConfig({ op: "attach-add", module: on, target: ref, octet })) { toast(`${ref} branchée sur ${on}`); load(true); }
}

export async function loadCore(fresh = false) {
  try { core = await (await api("/api/core" + (fresh ? "?fresh" : ""))).json(); renderCore(); }
  catch (e) { $("#view-core").innerHTML = `<div class="empty err">${esc(e.message)}</div>`; }
}

export async function loadPKI() {
  try { pki = await (await api("/api/pki")).json(); renderPKI(); }
  catch (e) { $("#view-pki").innerHTML = `<div class="empty err">${esc(e.message)}</div>`; }
}

// checkTrust sonde les VMs par SSH — à la demande seulement (bouton), jamais dans loadPKI ni au rafraîchissement.
export async function checkTrust() {
  if (running || trustChecking) return;
  trustChecking = true; renderPKI();
  try { pkiTrust = await (await api("/api/pki/trust")).json(); }
  catch (e) { toast(e.message, true); }
  finally { trustChecking = false; renderPKI(); render(); }
}

// trustLine : raccourci confiance d'une VM dans l'onglet VMs d'un module (pastilles du dernier « Vérifier » +
// boutons pour poser la racine ou l'autorité d'interception), pour ne pas quitter le module.
export function trustLine(lv, busy) {
  const t = pkiTrust && pkiTrust[lv];
  const pill = (ok, label) => !t ? `<span class="pill" title="pas encore vérifié">${label} ?</span>`
    : !t.reachable ? `<span class="pill" title="${esc(t.err || "injoignable")}">${label} —</span>`
    : `<span class="pill ${ok ? "on" : "never"}">${label} ${ok ? "✓" : "✗"}</span>`;
  return `<div class="actions" style="margin-top:4px;align-items:center">${pill(t && t.root, "racine")}${pill(t && t.interception, "interception")}
    <button class="linkbtn" ${busy} data-trust='${esc(JSON.stringify(["pki", "trust-vm", lv]))}'>poser racine</button>
    <button class="linkbtn" ${busy} data-trust='${esc(JSON.stringify(["pki", "trust-vm", "--interception", lv]))}'>poser interception</button></div>`;
}

export async function load(fresh = false) {
  try {
    state = await (await api("/api/state" + (fresh ? "?fresh" : ""))).json();
    render();
    $("#updated").textContent = "à jour à " + new Date().toLocaleTimeString("fr-FR");
  } catch (e) {
    $("#updated").innerHTML = `<span class="err">${esc(e.message)}</span>`;
  }
}

// running et current sont lus partout mais réaffectés par run.js : un module ne peut pas réaffecter une liaison importée.
export function setRunning(v) { running = v; }

export function setCurrent(v) { current = v; }
