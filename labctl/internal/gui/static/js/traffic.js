// GUI de labctl — page Trafic (proxy). Module ES : ce qu'il partage est exporté, ce qu'il utilise est importé.
import { $, api, esc, page, toast } from "./core.js";
import { fsize } from "./files.js";

// --- Page Trafic : journal du proxy de la passerelle (connexions via: proxy), accès à mitmweb
export const tr = { events: null, err: "", q: "", module: "", action: "" };
export async function loadTraffic() {
  if (!$("#tr-list")) renderTrafficShell();
  try { tr.events = await (await api("/api/proxy/log?n=500")).json(); tr.err = ""; }
  catch (e) { tr.err = e.message; }
  renderTraffic();
}
export function renderTrafficShell() {
  $("#view-traffic").innerHTML = `<section class="card">
    <div class="card-head"><h2>Trafic du proxy</h2>
      <span class="meta">connexions <span class="mono">via: proxy</span> : log (HTTPS non déchiffré), filter, intercept</span>
      <span class="spacer"></span>
      <button id="tr-web" title="Interface de mitmproxy : inspecter, modifier, rejouer (tunnel SSH, 127.0.0.1 seulement)">Ouvrir mitmweb</button>
      <button id="tr-refresh">↻</button></div>
    <form class="inline" id="tr-filters" style="border-bottom:1px solid var(--line)">
      <label>Module<select name="module"><option value="">tous</option></select></label>
      <label>Action<select name="action"><option value="">toutes</option><option>passé</option><option>refusé</option><option>intercepté</option></select></label>
      <label>Recherche<input name="q" placeholder="domaine, source, URL…" style="min-width:220px"></label>
      <span class="meta" id="tr-count"></span>
    </form>
    <div id="tr-list"><div class="empty">Chargement…</div></div>
    <div class="card-desc meta" style="border-top:1px solid var(--line)">Mode intercept : une VM ne déchiffre sans alerte que si elle fait confiance à
      l'autorité d'interception (page PKI, « Confiance dans les VMs ») ; sinon on observe son comportement face au certificat inconnu.</div>
  </section>`;
}
export function renderTraffic() {
  const box = $("#tr-list");
  if (!box) return;
  const mods = [...new Set((tr.events || []).map(e => e.module).filter(Boolean))].sort();
  const sel = $("#tr-filters").module;
  if (sel.options.length - 1 !== mods.length) sel.innerHTML = `<option value="">tous</option>` + mods.map(m => `<option ${m === tr.module ? "selected" : ""}>${esc(m)}</option>`).join("");
  if (tr.err) { box.innerHTML = `<div class="empty err">${esc(tr.err)}</div>`; return; }
  const q = tr.q.toLowerCase();
  const list = (tr.events || []).filter(e => (!tr.module || e.module === tr.module) && (!tr.action || e.action === tr.action) &&
    (!q || [e.host, e.src, e.url, e.dst, e.target].some(x => (x || "").toLowerCase().includes(q))));
  $("#tr-count").textContent = `${list.length} / ${(tr.events || []).length} événement(s)`;
  const cls = { "passé": "", "refusé": "never", "intercepté": "open" };
  box.innerHTML = list.length ? `<table><thead><tr><th>Heure</th><th>Source</th><th>Action</th><th>Requête</th><th>Résultat</th></tr></thead><tbody>
    ${list.slice(0, 500).map(e => `<tr>
      <td class="meta mono" style="white-space:nowrap">${esc((e.time || "").slice(11, 19))}</td>
      <td class="mono">${esc(e.src)}<div class="meta">${esc(e.module)} → ${esc(e.target)}</div></td>
      <td><span class="pill ${cls[e.action] || ""}">${esc(e.action)}</span><div class="meta">${esc(e.mode)}</div></td>
      <td style="max-width:520px;overflow-wrap:anywhere">${e.kind === "http" ? `<span class="mono">${esc(e.method)}</span> ${esc(e.url)}`
        : `<span class="meta">TLS</span> ${esc(e.host || "(sans nom)")} <span class="meta mono">${esc(e.dst)}</span>`}</td>
      <td class="meta">${e.status ? esc(e.status) + (e.size ? " · " + fsize(e.size) : "") : ""}${e.why ? esc(e.why) : ""}${e.error ? `<span class="err">${esc(e.error)}</span>` : ""}</td></tr>`).join("")}
    </tbody></table>` : `<div class="empty">Rien pour l'instant (aucune connexion via: proxy, ou pas encore de trafic).</div>`;
}
document.addEventListener("input", e => {
  const f = e.target.closest("#tr-filters");
  if (!f) return;
  Object.assign(tr, { q: f.q.value, module: f.module.value, action: f.action.value });
  renderTraffic();
});
document.addEventListener("change", e => { if (e.target.closest("#tr-filters")) { const f = $("#tr-filters"); Object.assign(tr, { module: f.module.value, action: f.action.value }); renderTraffic(); } });
document.addEventListener("submit", e => { if (e.target.id === "tr-filters") e.preventDefault(); });
document.addEventListener("click", async e => {
  if (e.target.id === "tr-refresh") loadTraffic();
  else if (e.target.id === "tr-web") {
    e.target.disabled = true;
    try {
      const d = await (await api("/api/proxy/web", { method: "POST" })).json();
      window.open(d.url, "_blank", "noopener");
      toast(`mitmweb ouvert (tunnel SSH) ; mot de passe si demandé : ${d.password}`);
    } catch (err) { toast(err.message, true); }
    e.target.disabled = false;
  }
});
