// GUI de labctl — onglet Réseau d'un module. Module ES : ce qu'il partage est exporté, ce qu'il utilise est importé.
import { $, esc, stLabel } from "./util.js";
import { config, modPage, running } from "./core.js";
import { connCard } from "./pending.js";

// --- Onglet Réseau d'un module : réservations DHCP, alias DNS, liens dans les deux sens (section dns du module.yaml)
export function renderNet() {
  const [name, sub] = modPage();
  if (sub !== "net") return;
  const c = config, busy = running ? "disabled" : "";
  if (!c) { $("#mod-net").innerHTML = `<div class="empty">Chargement…</div>`; return; }
  const mods = c.modules || [], m = mods.find(x => x.name === name);
  if (!m) { $("#mod-net").innerHTML = `<div class="empty">Pas de configuration réseau pour ${esc(name)}.</div>`; return; }
  let h = "";
  if (c.gateway !== "running") h += `<div class="banner"><span>Passerelle ${esc(stLabel(c.gateway))} : les modifications sont enregistrées dans le module.yaml et seront appliquées au prochain démarrage (page Passerelle, « Appliquer la configuration »).</span></div>`;
  else if (!(c.pending || []).length && !c.pendingError) h += `<div class="banner ok"><span>La passerelle est à jour avec la configuration des modules.</span></div>`;

  const aliases = Object.entries(m.aliases || {}).sort();
  const targets = (m.hosts || []).map(x => x.name);
  h += `<section class="card"><div class="card-head"><h2>Réservations DHCP et noms DNS</h2>
      <span class="meta mono">${esc(m.subnet)}.0/24 · ${esc(m.domain)}</span></div>
    <table><thead><tr><th>Nom</th><th>IP</th><th>MAC</th><th>Source</th><th></th></tr></thead><tbody>
    ${(m.hosts || []).sort((a, b) => a.ip.localeCompare(b.ip, undefined, { numeric: true })).map(x => `<tr>
      <td><strong>${esc(x.name)}</strong><span class="meta">.${esc(m.domain)}</span></td><td class="mono">${esc(x.ip)}</td>
      <td class="mono meta">${esc(x.mac || "— (DNS seulement)")}</td>
      <td class="meta">${x.source === "vm" ? "VM du module" : x.source.startsWith("attach:") ? `branchée (${esc(x.source.slice(7))})` : "réservation"}</td>
      <td>${x.source === "reservation" ? `<button class="danger" ${busy} data-cfg='${esc(JSON.stringify({ op: "host-del", module: m.name, name: x.name }))}'>Retirer</button>` : ""}</td></tr>`).join("")
      || `<tr><td colspan="5" class="empty">Aucune</td></tr>`}
    </tbody></table>
    <form class="inline" data-cfg-form="host" data-module="${esc(m.name)}" style="border-top:1px solid var(--line)">
      <strong style="width:100%">Réserver une IP (machine hors module.yaml, ex. VM importée)</strong>
      <label>Nom<input name="name" required pattern="[a-z0-9]([a-z0-9-]*[a-z0-9])?" placeholder="dc01"></label>
      <label>IP<span class="mono" style="display:flex;align-items:center;gap:2px">${esc(m.subnet)}.<input name="octet" type="number" min="2" max="99" required style="width:70px" placeholder="40"></span></label>
      <label>MAC<input name="mac" required pattern="([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}" placeholder="52:54:00:aa:bb:cc" class="mono"></label>
      <button ${busy}>Réserver</button>
      <span class="meta">IP fixe hors de la plage DHCP libre (.100-.199)</span>
    </form></section>`;

  h += `<section class="card"><div class="card-head"><h2>Alias DNS</h2><span class="meta">autre nom pour une VM : &lt;alias&gt;.${esc(m.domain)}</span></div>
    <table><thead><tr><th>Alias</th><th>Pointe vers</th><th></th></tr></thead><tbody>
    ${aliases.map(([a, t]) => `<tr><td><strong>${esc(a)}</strong><span class="meta">.${esc(m.domain)}</span></td><td class="mono">${esc(t)}</td>
      <td><button class="danger" ${busy} data-cfg='${esc(JSON.stringify({ op: "alias-del", module: m.name, name: a }))}'>Retirer</button></td></tr>`).join("")
      || `<tr><td colspan="3" class="empty">Aucun alias</td></tr>`}
    </tbody></table>
    <form class="inline" data-cfg-form="alias" data-module="${esc(m.name)}" style="border-top:1px solid var(--line)">
      <label>Alias<input name="name" required pattern="[a-z0-9]([a-z0-9-]*[a-z0-9])?" placeholder="intranet"></label>
      <label>Cible (VM ou nom complet)<input name="target" required list="targets-${esc(m.name)}" placeholder="web"></label>
      <datalist id="targets-${esc(m.name)}">${targets.map(x => `<option value="${esc(x)}">`).join("")}</datalist>
      <button ${busy}>Ajouter</button>
    </form></section>`;

  // Un lien est à sens unique : les deux sens sont montrés côte à côte, chacun avec sa case
  const others = mods.filter(x => x.name !== m.name);
  const box = (from, to) => {
    const on = (mods.find(x => x.name === from)?.links || []).includes(to);
    const restricted = !on && (mods.find(x => x.name === from)?.connections || []).some(c => c.to === to || c.to.startsWith(to + "/"));
    return `<label class="check" style="display:inline-flex;gap:6px;align-items:center"><input type="checkbox" ${on ? "checked" : ""} ${busy}
      data-cfg='${esc(JSON.stringify({ op: on ? "unlink" : "link", module: from, target: to }))}'> ${on ? "oui (tout)" : restricted ? "restreint (voir connexions)" : "non"}</label>`;
  };
  h += `<section class="card"><div class="card-head"><h2>Liens avec les autres modules</h2>
      <span class="meta">joindre = ouvrir des connexions et résoudre les noms DNS ; les réponses reviennent toujours. Chaque sens se règle à part.</span></div>
    ${others.length ? `<table><thead><tr><th>Module</th><th>${esc(m.name)} peut joindre ce module</th><th>ce module peut joindre ${esc(m.name)}</th></tr></thead><tbody>
      ${others.map(o => `<tr><td><strong>${esc(o.name)}</strong><div class="meta mono">${esc(o.subnet)}.0/24 · ${esc(o.domain)}</div></td>
        <td>${box(m.name, o.name)}</td><td>${box(o.name, m.name)}</td></tr>`).join("")}
    </tbody></table>` : `<div class="empty">Aucun autre module.</div>`}</section>`;
  h += connCard(m, mods, busy);
  $("#mod-net").innerHTML = h;
}
