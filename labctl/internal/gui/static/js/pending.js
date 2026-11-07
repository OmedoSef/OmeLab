// GUI de labctl — changements en attente sur la passerelle. Scripts classiques chargés dans l'ordre par index.html (portée globale
// partagée) : une fonction n'est utilisable qu'après le chargement du fichier qui la définit.
"use strict";

// --- Changements en attente sur la passerelle : bandeau en haut de toutes les pages
// Changements de la passerelle non appliqués : pilule compacte dans l'en-tête (cliquable), détail dans une
// modale. Seul le cas d'erreur garde une bannière rouge en haut du contenu.
function renderPending() {
  const c = config;
  if (!c) return;
  const pending = c.pending || [];
  const on = c.gateway === "running";
  $("#pending").innerHTML = (on && c.pendingError) ? `<div class="banner"><span class="err">${esc(c.pendingError)}</span></div>` : "";
  const pill = $("#pending-pill"), dlg = $("#pending-dlg");
  if (on && pending.length) {
    pill.hidden = false;
    pill.innerHTML = `⚠ ${pending.length} changement${pending.length > 1 ? "s" : ""} passerelle`;
    if (dlg.open) dlg.innerHTML = pendingModal(); // garder la modale à jour si elle est ouverte
  } else {
    pill.hidden = true;
    if (dlg.open) dlg.close();
  }
}

function pendingModal() {
  const pending = (config && config.pending) || [], busy = running ? "disabled" : "";
  return `<div class="card-head"><h3 style="margin:0;font-size:16px">Modifications non appliquées sur la passerelle</h3>
      <span class="spacer"></span><button data-close-pending aria-label="Fermer">×</button></div>
    <div class="card-desc meta">${pending.length} fichier(s) de configuration changeront. « Appliquer » régénère puis recharge le DHCP, le DNS et le pare-feu de la passerelle.</div>
    ${pending.map(f => `<pre class="diff"><strong>${esc(f.file)}</strong>\n${f.lines.map(l =>
      l.op === "…" ? `<span class="skip">  …</span>` : `<span class="${l.op === "+" ? "add" : l.op === "-" ? "del" : ""}">${esc(l.op)} ${esc(l.text)}</span>`).join("\n")}</pre>`).join("")}
    <div class="pending-actions">
      <button data-close-pending>Fermer</button>
      <button class="primary" ${busy} data-apply-pending>Appliquer sur la passerelle</button>
    </div>`;
}

function openPending() {
  const dlg = $("#pending-dlg");
  dlg.innerHTML = pendingModal();
  dlg.showModal();
}

// Connexions restreintes (connections: du module.yaml) : celles du module, et celles qui arrivent vers lui
function connCard(m, mods, busy) {
  const fixed = mod => (mods.find(x => x.name === mod)?.hosts || []).filter(x => x.ip).map(x => x.name).sort();
  const desc = c => `${(c.from || []).length ? esc(c.from.join(", ")) : `<span class="meta">tout le module</span>`}`;
  const ports = c => (c.ports || []).length ? `<div class="tags">${c.ports.map(p => `<span class="pill mono">${esc(p)}</span>`).join("")}</div>` : `<span class="meta">tous</span>`;
  const out = m.connections || [];
  const mode = c => c.via === "proxy" ? `<span class="pill open" title="${esc(JSON.stringify(c.proxy || {}))}">proxy : ${esc(c.proxy?.mode || "log")}${
      (c.proxy?.allow || []).length ? " (seuls " + esc(c.proxy.allow.join(", ")) + ")" : (c.proxy?.deny || []).length ? " (sauf " + esc(c.proxy.deny.join(", ")) + ")" : ""}</span>`
    : c.via !== "portal" ? `<span class="meta">direct</span>`
    : `<span class="pill open" title="${esc(JSON.stringify(c.portal || {}))}">portail : ${esc(c.portal?.auth || "click")}, ${esc(c.portal?.duration || "1h")}</span>`;
  const incoming = [];
  for (const o of mods) if (o.name !== m.name) (o.connections || []).forEach(c => {
    if (c.to === m.name || c.to.startsWith(m.name + "/")) incoming.push({ ...c, src: o.name });
  });
  const targets = [["internet", "Internet"]];
  for (const o of mods) if (o.name !== m.name) {
    targets.push([o.name, `${o.name} (tout le module)`]);
    for (const vm of fixed(o.name)) targets.push([`${o.name}/${vm}`, `${o.name} / ${vm}`]);
  }
  return `<section class="card"><div class="card-head"><h2>Connexions restreintes</h2>
      <span class="meta">ports précis, depuis certaines VMs, vers un module, une VM ou Internet. Une connexion <strong>ouvre</strong> ses ports (même « accès direct » fermé) ; un lien de la carte précédente, lui, ouvre tout</span></div>
    <table><thead><tr><th>Vers</th><th>Depuis (${esc(m.name)})</th><th>Ports</th><th>Mode</th><th></th></tr></thead><tbody>
    ${out.map((c, i) => `<tr><td class="mono">${c.to === "internet" ? "Internet" : esc(c.to)}${c.local ? ` <span class="pill" title="connexion locale, non distribuée (network.local.yaml)">local</span>` : ""}</td><td>${desc(c)}</td><td>${ports(c)}</td><td>${mode(c)}</td>
      <td><button class="danger" ${busy} data-cfg='${esc(JSON.stringify({ op: "conn-del", module: m.name, index: i }))}'>Retirer</button></td></tr>`).join("")
      || `<tr><td colspan="5" class="empty">Aucune</td></tr>`}
    </tbody></table>
    ${m.vulnerable ? `<div class="meta" style="padding:10px 16px;border-top:1px solid var(--line)">Module vulnérable : aucune connexion sortante (Internet, autre module). Les autres modules, eux, peuvent déclarer une connexion vers lui.</div>` : `<form class="inline" data-cfg-form="conn" data-module="${esc(m.name)}" style="border-top:1px solid var(--line)">
      <strong style="width:100%">Ajouter une connexion</strong>
      <label>Vers<select name="target" required>${targets.map(([v, l]) => `<option value="${esc(v)}">${esc(l)}</option>`).join("")}</select></label>
      <span style="display:grid;gap:3px;font-size:12px;color:var(--muted)">Depuis (aucune case : tout le module)<span style="display:flex;flex-wrap:wrap;gap:4px 12px;padding-top:4px">
        ${fixed(m.name).map(n => `<label class="check"><input type="checkbox" name="from" value="${esc(n)}"> ${esc(n)}</label>`).join("") || `<span class="meta">aucune VM à IP fixe</span>`}</span></span>
      <label>Ports (vide : tous)<input name="ports" class="mono" placeholder="tcp/80 tcp/443 udp/53 icmp" style="min-width:240px"></label>
      <label>Mode<select name="via" data-via><option value="direct">direct</option><option value="portal">derrière un portail captif</option><option value="proxy">via le proxy</option></select></label>
      <div class="proxy-opts" hidden style="width:100%;display:flex;flex-wrap:wrap;gap:8px;align-items:end;padding:8px 0;border-top:1px dashed var(--line)">
        <label>Mode du proxy<select name="pmode" data-pmode><option value="log">log : journal, HTTPS non déchiffré</option><option value="filter">filter : domaines autorisés / refusés</option><option value="intercept">intercept : HTTPS déchiffré</option></select></label>
        <label class="pxf" hidden>Liste<select name="plist"><option value="allow">seuls ces domaines</option><option value="deny">tout sauf ces domaines</option></select></label>
        <label class="pxf" hidden>Domaines<input name="pdomains" class="mono" placeholder="*.debian.org github.com" style="min-width:240px"></label>
        <label>Autres ports HTTP(S)<input name="pports" class="mono" placeholder="tcp/8080" style="width:120px"></label>
        <span class="meta">80 et 443 passent toujours par le proxy ; les autres ports suivent « Ports ». Le filtrage par domaine se fie au nom annoncé par le client : il sert à observer, pas à contenir une machine hostile</span>
      </div>
      <div class="portal-opts" hidden style="width:100%;display:flex;flex-wrap:wrap;gap:8px;align-items:end;padding:8px 0;border-top:1px dashed var(--line)">
        <label>Authentification<select name="auth" data-auth><option value="click">un clic</option><option value="password">mot de passe</option><option value="terms">accepter les conditions</option></select></label>
        <label class="pw" hidden>Mot de passe<input name="password" autocomplete="off"></label>
        <label>Durée d'une session<input name="duration" value="1h" pattern="[0-9]+(m|h)" title="ex. 30m, 2h (1m à 24h)" style="width:80px"></label>
        <label>Titre<input name="title" placeholder="Portail du lab"></label>
        <label>HTTPS non authentifié<select name="https"><option value="">refusé (reject)</option><option value="drop">sans réponse (drop)</option></select></label>
        <label class="check" title="Redirige les tests de connectivité d'Android, Apple, Windows… : le système affiche « Se connecter au réseau »"><input type="checkbox" name="detect" checked> détection par les OS</label>
        <label class="check" title="RFC 8910 / 8908 : option DHCP 114 et API JSON du portail"><input type="checkbox" name="api"> annonce DHCP 114 + API</label>
      </div>
      <label class="check" style="width:100%" title="Écrit la connexion dans network.local.yaml (propre à ce poste, non distribué) au lieu du module.yaml — pour les bascules de test et les VMs branchées localement"><input type="checkbox" name="local"> local à ce poste (non distribué)</label>
      <button ${busy}>Ajouter</button>
    </form>`}
    ${incoming.length ? `<div class="card-head" style="border-top:1px solid var(--line);border-bottom:0;display:block">
      <strong>Vers ${esc(m.name)}, déclarées par d'autres modules</strong> <span class="meta">(à modifier dans le module source)</span>
      ${incoming.map(c => `<div style="display:flex;gap:10px;align-items:center;margin-top:6px;flex-wrap:wrap">
        <span class="mono">${esc(c.src)}${(c.from || []).length ? "[" + esc(c.from.join(",")) + "]" : ""} → ${esc(c.to)}</span> ${ports(c)}
        <button class="linkbtn" style="font-weight:400" data-go="m/${esc(c.src)}/net">ouvrir ${esc(c.src)}</button></div>`).join("")}</div>` : ""}
  </section>`;
}
