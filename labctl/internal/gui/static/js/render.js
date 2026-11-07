// GUI de labctl — rendu : vue d'ensemble, barre latérale, pages de module. Scripts classiques chargés dans l'ordre par index.html (portée globale
// partagée) : une fonction n'est utilisable qu'après le chargement du fichier qui la définit.
"use strict";

// --- Rendu
const has = (m, c) => (m.commands || []).includes(c);
const runAttr = args => `data-run='${esc(JSON.stringify(args))}'`;
const vmCount = m => { const vms = m.vms || []; return [vms.filter(v => v.state === "running").length, vms.length]; };

// Actions qui portent sur tout le module (vue d'ensemble et page du module)
function moduleActions(m, busy) {
  const vms = m.vms || [], wide = activeOn(m.name, "", true);
  if (wide) return doing(wide);
  const anyUp = vms.some(v => v.state === "running"), anyDown = vms.some(v => v.state !== "running" && v.state !== "absente");
  const anyMissing = vms.some(v => v.state === "absente");
  return `<span class="actions">
    ${has(m, "start") && anyDown ? `<button ${busy} ${runAttr([m.name, "start"])}>Tout démarrer</button>` : ""}
    ${has(m, "stop") && anyUp ? `<button ${busy} ${runAttr([m.name, "stop"])} data-confirm="Arrêter toutes les VMs du module ${esc(m.name)} ?">Tout arrêter</button>` : ""}
    ${has(m, "up") ? `<button ${busy} class="${anyMissing ? "primary" : ""}" ${runAttr([m.name, "up"])}
        data-confirm="${anyMissing ? `Construire le module ${esc(m.name)} : réseau, CA et VMs manquantes (les VMs existantes ne sont pas recréées).` : `Mettre à jour le module ${esc(m.name)} : réconcilie réseau, CA, certificats et cartes branchées avec le module.yaml.`}"
        title="labctl ${esc(m.name)} up : crée ce qui manque, réconcilie le reste">${anyMissing ? "Construire" : "Mettre à jour"}</button>` : ""}
  </span>`;
}

function render() {
  if (!state) return;
  const gwUp = state.gateway.state === "running";
  $("#gw").className = gwUp ? "" : "down";
  $("#gw").innerHTML = `<span class="dot ${gwUp ? "running" : ""}"></span>passerelle ${esc(stLabel(state.gateway.state))}`;
  const mods = state.modules || [];
  const busy = running ? "disabled" : "";
  const [cur, sub] = modPage();

  // Panneau latéral
  const nav = (p, label, extra = "") => `<button data-go="${esc(p)}" class="${page === p || (p.startsWith("m/") && cur && p.startsWith(`m/${cur}/`) && page !== "new") ? "active" : ""}">${label}${extra}</button>`;
  $("#side").innerHTML = nav("overview", "Vue d'ensemble") +
    `<div class="grp">Modules</div>` +
    mods.map(m => {
      const [up, n] = vmCount(m);
      return nav(`m/${m.name}/vms`, `<span class="dot ${up ? "running" : ""}"></span>${esc(m.name)}`, `<span class="n">${activeOn(m.name) ? `<span class="spin"></span>` : ""}${up}/${n}</span>`);
    }).join("") +
    (ed.create ? `<button data-go="new" class="${page === "new" ? "active" : ""}"><span class="dot absente"></span>${esc(ed.name)}<span class="n">brouillon</span></button>` : "") +
    `<button class="add" data-new-module>+ Nouveau module…</button>` +
    `<div class="grp">Infrastructure</div>` +
    nav("core", `<span class="dot ${gwUp ? "running" : ""}"></span>Passerelle`) +
    nav("pki", "PKI") +
    nav("traffic", "Trafic (proxy)");

  // Sans passerelle, plus de DHCP, DNS ni Internet : on le dit en tête, avec de quoi la relancer
  const down = gwUp ? "" : `<div class="banner down-banner"><strong>Passerelle core-gw ${esc(stLabel(state.gateway.state))}</strong>
    <span class="meta">les modules n'ont plus de DHCP, de DNS ni d'Internet</span><span class="spacer"></span>
    ${state.gateway.state === "absente" ? `<button data-go="core">Page Passerelle</button>` : `<button class="primary" ${busy} data-run='["core","start"]'>Démarrer la passerelle</button>`}</div>`;

  if (page === "overview") renderOverview(mods, down, busy);
  if (cur) renderModule(mods.find(m => m.name === cur), cur, sub, down, gwUp, busy);
}

function renderOverview(mods, down, busy) {
  const links = [];
  for (const a of (config?.modules || [])) for (const b of a.links || []) {
    const back = (config.modules.find(x => x.name === b)?.links || []).includes(a.name);
    if (back && a.name > b) continue; // lien réciproque : une seule fois
    links.push(`<span class="pill mono">${esc(a.name)} ${back ? "↔" : "→"} ${esc(b)}</span>`);
  }
  $("#view-overview").innerHTML = down + `<section class="card">
    <div class="card-head"><h2>Modules</h2><span class="meta">${mods.length} module(s)</span><span class="spacer"></span>
      <button data-new-module title="Ouvre l'éditeur sur le squelette d'un nouveau module.yaml">Nouveau module…</button></div>
    <table><thead><tr><th>Module</th><th>Réseau</th><th>VMs</th><th>Internet</th><th>Actions</th></tr></thead><tbody>
    ${mods.map(m => {
      const [up, n] = vmCount(m);
      return `<tr><td><button class="linkbtn" data-go="m/${esc(m.name)}/vms">${esc(m.name)}</button><div class="meta">${esc(m.description)}</div></td>
        <td class="mono meta">${esc(m.subnet)}.0/24<div>${esc(m.domain)}</div>${m.netActive ? "" : `<span class="pill never">réseau inactif</span>`}</td>
        <td><span class="vmdots">${(m.vms || []).map(v => `<span class="dot ${esc(v.state.replace(" ", "-"))}" title="${esc(v.name)} : ${esc(stLabel(v.state))}"></span>`).join("")}</span>
          <span class="meta">${up}/${n}</span></td>
        <td><span class="pill ${m.internetOpen ? "open" : ""}">${esc(m.internet)}</span></td>
        <td>${moduleActions(m, busy)}</td></tr>`;
    }).join("") || `<tr><td colspan="5" class="empty">Aucun module : « Nouveau module… » pour en créer un.</td></tr>`}
    </tbody></table>
    <div class="card-head" style="border-top:1px solid var(--line);border-bottom:0">
      <span class="meta">Liens entre modules :</span>
      <span class="tags">${links.join("") || `<span class="meta">aucun (chaque module est isolé)</span>`}</span>
      <span class="meta">→ un sens, ↔ les deux ; à régler dans l'onglet Réseau de chaque module</span>
    </div>
  </section>`;
}

function renderModule(m, name, sub, down, gwUp, busy) {
  const dis = running || !gwUp ? "disabled" : "";
  const yamlMod = page === "new" || (m && has(m, "up"));
  const tabs = page === "new" ? [["yaml", "module.yaml"]]
    : [["vms", "VMs"], ...(yamlMod ? [["yaml", "module.yaml" + (ed.name === name && dirty() ? " ●" : "")],
        ["files", "Fichiers" + (fx.module === name && fdirty() ? " ●" : "")]] : []), ["net", "Réseau"]];
  let head;
  if (page === "new") head = `<div class="card-head"><h2>${esc(name)}</h2><span class="pill">nouveau module, pas encore enregistré</span></div>`;
  else if (!m) head = `<div class="card-head"><h2>${esc(name)}</h2><span class="err">module introuvable</span></div>`;
  else {
    const openAll = m.internetAll;
    head = `<div class="card-head">
        <h2>${esc(m.name)}</h2>
        <span class="meta mono">${esc(m.subnet)}.0/24 · ${esc(m.domain)}</span>
        ${m.netActive ? "" : `<span class="pill never">réseau ${esc(m.net)} inactif</span>`}
        <span class="spacer"></span>${moduleActions(m, busy)}
      </div>
      <div class="card-head" style="padding-top:8px;padding-bottom:8px">
        <span class="meta">${esc(m.description)}</span>
        <span class="spacer"></span>
        <span class="pill ${m.internetOpen ? "open" : ""}" title="Accès complet accordé à la main (core internet on) aux VMs sans connexion dédiée. Les connexions déclarées (direct, proxy, portail) ouvrent leurs ports indépendamment de cet état.">Accès direct : ${esc(m.internet)}</span>
        <span class="actions">
          ${m.vulnerable
            ? `<span class="pill never" title="vulnerable: true dans le module.yaml : Internet n'y est jamais ouvert à la main (seulement pendant l'installation d'une VM internet: install), rien ne sort du module">module vulnérable : jamais d'Internet</span>
               ${m.internetOpen ? `<button ${dis} ${runAttr(["core", "internet", "off", m.name])}>Tout fermer</button>` : ""}`
            : openAll
            ? `<button ${dis} ${runAttr(["core", "internet", "off", m.name])}>Fermer l'accès direct</button>`
            : `<button ${dis} ${runAttr(["core", "internet", "on", m.name])} data-confirm="Donner un accès Internet COMPLET à toutes les VMs de ${esc(m.name)} (en plus des connexions déclarées ; sauf les VMs « jamais ») ?">Ouvrir à tout le module</button>
               ${m.internetOpen ? `<button ${dis} ${runAttr(["core", "internet", "off", m.name])}>Tout fermer</button>` : ""}`}
        </span>
      </div>`;
  }
  $("#mod-head").innerHTML = head + `<nav class="subtabs">${tabs.map(([k, l]) =>
    `<button data-go="${page === "new" ? "new" : `m/${esc(name)}/${k}`}" class="${sub === k ? "active" : ""}">${esc(l)}</button>`).join("")}</nav>`;
  if (sub !== "vms" || !m) { if (sub === "vms") $("#mod-vms").innerHTML = ""; return; }

  const vms = m.vms || [], wide = activeOn(m.name, "", true), openAll = m.internetAll;
  const pkiOn = !!(pki && pki.pki && pki.pki.initialized);
  let html = (down ? down : "") + `<section class="card">${pkiOn ? `<div class="card-head" style="padding-top:8px;padding-bottom:8px">
      <span class="meta">confiance PKI : pastilles du dernier « Vérifier » (sonde SSH à la demande, jamais au rafraîchissement)</span>
      <span class="spacer"></span>
      <button ${running || trustChecking ? "disabled" : ""} data-check-trust>${trustChecking ? "Vérification…" : "Vérifier la confiance"}</button></div>` : ""}<table>
    <thead><tr><th>VM</th><th>IP</th><th>Snapshots</th><th>Internet</th><th>Actions</th></tr></thead><tbody>`;
  for (const v of vms) {
    const up = v.state === "running", exists = v.state !== "absente";
    const snaps = v.snapshots || [];
    const act = wide ? "" : activeOn(m.name, v.name);
    let net;
    if (v.noInternet) net = `<span class="pill never" title="internet: never dans le module.yaml">jamais</span>`;
    else if (m.vulnerable && !v.internet && !openAll) net = `<span class="pill never" title="module vulnérable : Internet seulement pendant l'installation (internet: install)">fermé</span>`;
    else if (openAll) net = `<span class="pill open">ouvert (module)</span>`;
    else if (v.internet) net = `<span class="pill open">ouvert</span> <button ${dis} ${runAttr(["core", "internet", "off", m.name, v.name])}>Fermer</button>`;
    else net = `<span class="pill">fermé</span> <button ${dis} ${runAttr(["core", "internet", "on", m.name, v.name])}>Ouvrir</button>`;
    html += `<tr>
      <td><span class="dot ${esc(v.state.replace(" ", "-"))}"></span><strong>${esc(v.name)}</strong> <span class="meta">${esc(stLabel(v.state))}</span>
        ${pkiOn && exists && v.libvirt ? trustLine(v.libvirt, busy) : ""}</td>
      <td class="mono">${esc(v.ip)}</td>
      <td class="meta">${snaps.length ? esc(snaps.join(", ")) : "—"}</td>
      <td>${net}</td>
      <td><div class="actions">${act ? doing(act) : !exists
        ? (has(m, "up") ? `<button ${busy} ${runAttr([m.name, "up", v.name])}>Créer</button>`
          : has(m, v.name) ? `<button ${busy} ${runAttr([m.name, v.name])}>Créer</button>` : `<span class="meta">absente</span>`)
        : `${has(m, "start") && !up ? `<button ${busy} ${runAttr([m.name, "start", v.name])}>Démarrer</button>` : ""}
           ${has(m, "stop") && up ? `<button ${busy} ${runAttr([m.name, "stop", v.name])}>Arrêter</button>` : ""}
           ${up ? `<button data-console="${esc(v.libvirt)}">Console</button>` : ""}
           ${has(m, "snapshot") ? `<button ${busy} data-snapshot='${esc(JSON.stringify([m.name, v.name]))}'>Snapshot</button>` : ""}
           ${has(m, "restore") && snaps.length ? `<button class="danger" ${busy} data-restore='${esc(JSON.stringify([m.name, v.name, snaps]))}'>Restaurer…</button>` : ""}
           ${has(m, "destroy") ? `<button class="danger" ${busy} ${runAttr([m.name, "destroy", v.name])} data-confirm="Supprimer ${esc(v.name)} : la VM, son disque et ses snapshots seront définitivement effacés. Irréversible.">Supprimer</button>` : ""}`}
      </div></td></tr>`;
  }
  if (!vms.length) html += `<tr><td colspan="5" class="empty">Aucune VM déclarée : onglet module.yaml</td></tr>`;
  // VMs d'autres modules branchées sur ce réseau (attach:) : la machine reste gérée dans son module
  html += `<tr class="sub"><td colspan="5" class="meta">VMs externes branchées sur ce réseau</td></tr>`;
  for (const a of m.attached || []) {
    const [owner] = a.from.split("/");
    const detach = a.local
      ? `<button class="danger" ${busy} data-detach='${esc(JSON.stringify({ module: m.name, vm: a.from, name: a.name }))}'>Débrancher</button>`
      : `<span class="meta" title="déclaré dans ${esc(owner)}/module.yaml (attach:)">dans le module.yaml</span>`;
    html += `<tr>
      <td><span class="dot ${esc(a.state.replace(" ", "-"))}"></span><strong>${esc(a.name)}</strong> <span class="meta">${esc(stLabel(a.state))}</span>
        <div class="meta">branchée depuis <button class="linkbtn" style="font-weight:400" data-go="m/${esc(owner)}/vms">${esc(a.from)}</button></div></td>
      <td class="mono">${esc(a.ip)}</td><td class="meta">—</td><td><span class="meta">via ${esc(owner)}</span></td>
      <td><div class="actions">${a.state === "absente" ? `<span class="meta">à créer dans ${esc(owner)}</span>`
        : a.plugged ? `<span class="pill on">carte branchée</span>`
        : `<button ${busy} ${runAttr([m.name, "up"])} title="labctl ${esc(m.name)} up : pose la carte de cette VM sur le réseau du module">Poser la carte</button>`} ${detach}</div></td></tr>`;
  }
  html += `<tr><td colspan="5"><button ${busy} data-attach-add="${esc(m.name)}">+ Brancher une VM d'un autre module…</button></td></tr>`;
  html += `</tbody></table>
    <details class="cmd" style="border-top:1px solid var(--line)" ${openCmd.has(m.name) ? "open" : ""} data-cmd-box="${esc(m.name)}">
      <summary>Autre commande du module (labctl ${esc(m.name)} …)</summary>
      <div class="card-head" style="border-bottom:0">
        <input class="mono" style="flex:1;min-width:200px" placeholder="ex. status  ·  certs  ·  snapshot msf2 avant-exploit" data-cmd="${esc(m.name)}">
        <button ${busy} data-cmd-run="${esc(m.name)}">Lancer</button>
      </div>
    </details></section>`;

  const focus = document.activeElement?.dataset?.cmd, val = focus ? document.activeElement.value : "";
  $("#mod-vms").innerHTML = html;
  if (focus) { const el = $(`[data-cmd="${CSS.escape(focus)}"]`); if (el) { el.value = val; el.focus(); } }
}
