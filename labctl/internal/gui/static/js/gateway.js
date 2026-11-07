// GUI de labctl — page Passerelle. Scripts classiques chargés dans l'ordre par index.html (portée globale
// partagée) : une fonction n'est utilisable qu'après le chargement du fichier qui la définit.
"use strict";

// --- Vue Core : passerelle, services, DHCP, pare-feu
function renderCore() {
  const c = core, busy = running ? "disabled" : "";
  if (!c) return;
  const up = c.state === "running", absent = c.state === "absente";
  let h = `<section class="card">
    <div class="card-head"><h2>Passerelle core-gw</h2>
      <span class="pill"><span class="dot ${up ? "running" : ""}"></span>${esc(stLabel(c.state))}</span>
      <span class="spacer"></span>
      <span class="actions">${absent
        ? `<button class="primary" ${busy} data-run='["core","gw"]'>Créer la passerelle</button>`
        : `${up
            ? `<button ${busy} data-run='["core","apply"]' title="Régénère réseau, DHCP, DNS et pare-feu depuis les module.yaml">Appliquer la configuration</button>
               <button ${busy} data-run='["core","update"]' title="Pousse la version courante des fichiers système puis applique">Mettre à jour</button>
               <button data-console="core-gw">Console</button>
               <button class="danger" ${busy} data-run='["core","stop"]' data-confirm="Arrêter la passerelle ? Les modules perdent DHCP, DNS et Internet.">Arrêter</button>`
            : `<button class="primary" ${busy} data-run='["core","start"]'>Démarrer</button>`}`}
      </span>
    </div>
    <dl class="kv">
      <dt>WAN (NAT libvirt)</dt><dd class="mono">${esc(c.wan || "—")}</dd>
      <dt>Réseaux branchés</dt><dd>${(c.networks || []).map(n => `<span class="pill">${esc(n)}</span>`).join(" ") || "—"}</dd>
      ${c.error ? `<dt>Erreur</dt><dd class="err">${esc(c.error)}</dd>` : ""}
      ${up && !c.error ? `<dt>labctl (portails)</dt><dd>${c.labctlStale
        ? `<span class="err">${c.labctl ? `version ${esc(c.labctl)}, hôte en ${esc(c.hostLabctl)}` : "absent de la passerelle"}</span>
           <button ${busy} data-run='["core","update"]' title="Copie le labctl de l'hôte sur la passerelle, avec ses fichiers système">Mettre à jour la passerelle</button>`
        : `<span class="mono">${esc(c.labctl)}</span> <span class="meta">même version que l'hôte</span>`}</dd>` : ""}
    </dl>
  </section>`;
  if (absent) { h += `<section class="card"><div class="empty">La passerelle fournit DHCP, DNS et Internet à tous les modules : la créer d'abord.</div></section>`; }
  if (up) {
    h += `<section class="card"><div class="card-head"><h2>Services</h2>
      <span class="meta">dnsmasq : un DHCP commun, deux DNS par module (ouvert / fermé) ; nftables : pare-feu</span></div>
      <table><thead><tr><th>Service</th><th>État</th><th>Rôle</th></tr></thead><tbody>
      ${(c.services || []).map(sv => `<tr><td class="mono"><span class="dot ${sv.active === "active" ? "active" : sv.active === "failed" ? "failed" : ""}"></span>${esc(sv.name)}</td>
        <td>${esc(sv.active)} <span class="meta">(${esc(sv.sub)})</span></td><td class="meta">${esc(sv.role)}</td></tr>`).join("")
        || `<tr><td colspan="3" class="empty">Aucun service trouvé</td></tr>`}
      </tbody></table></section>`;
    h += `<section class="card"><div class="card-head"><h2>Baux DHCP</h2><span class="meta">${(c.leases || []).length} bail(s)</span></div>
      <table><thead><tr><th>IP</th><th>Nom</th><th>Module</th><th>MAC</th><th>Expire</th></tr></thead><tbody>
      ${(c.leases || []).map(l => `<tr><td class="mono">${esc(l.ip)}</td><td>${esc(l.name || "—")}</td><td>${esc(l.module || "—")}</td>
        <td class="mono meta">${esc(l.mac)}</td><td class="meta">${esc(l.expires)}</td></tr>`).join("")
        || `<tr><td colspan="5" class="empty">Aucun bail</td></tr>`}
      </tbody></table></section>`;
    h += `<section class="card"><div class="card-head"><h2>Pare-feu</h2></div>
      <dl class="kv">
        <dt>Autorisées vers Internet</dt><dd class="tags">${(c.allowed || []).map(a => `<span class="pill open mono">${esc(a)}</span>`).join("") || `<span class="meta">aucune : Internet fermé partout</span>`}</dd>
        <dt>Bloquées en permanence</dt><dd class="tags">${(c.blocked || []).map(a => `<span class="pill never mono">${esc(a)}</span>`).join("") || "—"}</dd>
        <dt>Toujours</dt><dd class="meta">LAN et réseaux privés injoignables, rien n'entre depuis le WAN sauf SSH, aucun routage entre modules sans lien</dd>
      </dl></section>`;
    const ps = c.portal || [];
    h += `<section class="card"><div class="card-head"><h2>Portails captifs</h2>
        <span class="meta">sources authentifiées (connexions <span class="mono">via: portal</span>) ; à l'expiration, la source est de nouveau captée</span></div>
      <table><thead><tr><th>Portail</th><th>Source</th><th>Module → cible</th><th>Depuis</th><th>Expire</th><th></th></tr></thead><tbody>
      ${ps.map(p => `<tr><td class="mono">${p.portal}</td><td class="mono">${esc(p.ip)}</td><td>${esc(p.module)} → ${esc(p.target)}</td>
        <td class="meta">${new Date(p.since).toLocaleTimeString("fr-FR")}</td>
        <td>${new Date(p.expires).toLocaleTimeString("fr-FR")} <span class="meta">(${Math.max(0, Math.round((new Date(p.expires) - Date.now()) / 60000))} min)</span></td>
        <td><button class="danger" ${busy} data-run='${esc(JSON.stringify(["core", "portal", "kick", String(p.portal), p.ip]))}'>Déconnecter</button></td></tr>`).join("")
        || `<tr><td colspan="6" class="empty">Aucune session</td></tr>`}
      </tbody></table></section>`;
  }
  $("#view-core").innerHTML = h;
}
