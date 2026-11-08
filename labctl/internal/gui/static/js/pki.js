// GUI de labctl — page PKI. Module ES : ce qu'il partage est exporté, ce qu'il utilise est importé.
import { $, esc, toast } from "./util.js";
import { checkTrust, editConfig, pki, pkiTrust, running, trustChecking } from "./core.js";
import { run } from "./run.js";

// --- Vue PKI : CA, certificats, émission, déploiement
const kindLabel = { root: "racine", intermediate: "intermédiaire", "sub-ca": "CA dérivée", server: "serveur + client", client: "client" };
function expiry(c) {
  const cls = c.daysLeft < 30 ? "err" : c.daysLeft < 90 ? "warn" : "meta";
  return `<span class="${cls}">${esc(c.notAfter)} (${c.daysLeft} j)</span>`;
}
export function files(c) {
  return (c.files || []).map(f => `<button data-download="${esc(f)}" title="${esc(f)}">${esc(f.split("/").pop())}</button>`).join(" ");
}
export function renderPKI() {
  const p = pki && pki.pki, vms = (pki && pki.vms) || [], busy = running ? "disabled" : "";
  if (!p) return;
  if (!p.initialized) {
    $("#view-pki").innerHTML = `<section class="card"><div class="card-head"><h2>PKI</h2></div>
      <div class="empty">Pas encore de PKI : la CA racine « ${esc(p.org)} Root CA » (20 ans) et la CA intermédiaire (10 ans) seront créées, pour le domaine ${esc(p.domain)}.
      <p><button class="primary" ${busy} data-run='["pki","init"]' data-confirm="Créer la CA racine et la CA intermédiaire ?">Initialiser la PKI</button></p></div></section>`;
    return;
  }
  const subs = p.cas.filter(c => c.kind === "sub-ca");
  const vmOpts = vms.map(v => `<option>${esc(v)}</option>`).join("");
  let h = `<section class="card"><div class="card-head"><h2>Autorités de certification</h2>
      <span class="meta">les VMs créées par labctl font confiance à la racine</span></div>
    <table><thead><tr><th>CA</th><th>Type</th><th>Limites</th><th>Expire</th><th>Fichiers publics</th></tr></thead><tbody>
    ${p.cas.map(c => `<tr><td><strong>${esc(c.subject)}</strong>${c.issuer !== c.subject ? `<div class="meta">signée par ${esc(c.issuer)}</div>` : ""}</td>
      <td>${esc(kindLabel[c.kind] || c.kind)}</td>
      <td><div class="tags">${(c.constraints || []).map(x => `<span class="pill mono">${esc(x)}</span>`).join("") || `<span class="meta">—</span>`}</div></td>
      <td>${expiry(c)}</td><td><div class="actions">${files(c)}</div></td></tr>`).join("")}
    </tbody></table>
    <form class="inline" data-form="subca">
      <strong style="width:100%">Nouvelle CA dérivée</strong>
      <label>Nom<input name="name" required pattern="[A-Za-z0-9][A-Za-z0-9-]*" placeholder="k8s"></label>
      <label>Domaine autorisé<input name="domain" required placeholder="k8s.${esc(p.domain)}"></label>
      <label>Sous-réseau autorisé (IP)<input name="subnet" placeholder="10.70.70.0/24"></label>
      <button ${busy}>Créer</button>
    </form>
  </section>`;

  h += `<section class="card"><div class="card-head"><h2>Autorité d'interception (proxy)</h2>
      <span class="meta">racine à part, utilisée par le proxy en mode <span class="mono">intercept</span> ; installée d'office sur la passerelle.
        À n'installer que sur les VMs qu'on veut observer (section <em>Confiance dans les VMs</em> ci-dessous), les autres doivent voir l'erreur.</span></div>
    ${p.interception ? `<table><thead><tr><th>CA</th><th>Expire</th><th>Fichier public</th></tr></thead><tbody>
      <tr><td><strong>${esc(p.interception.subject)}</strong></td><td>${expiry(p.interception)}</td><td><div class="actions">${files(p.interception)}</div></td></tr>
      </tbody></table>
      <div class="meta" style="padding:10px 16px;border-top:1px solid var(--line)">Pour qu'un navigateur (magasin propre à Firefox/Chrome, distinct du système) ne voie pas d'erreur en mode intercept, y importer ce certificat.</div>`
      : `<div class="empty">Pas encore générée : elle est créée automatiquement au premier montage du proxy en mode <span class="mono">intercept</span>.
        Tu peux la créer dès maintenant pour la télécharger à l'avance.
        <p><button class="primary" ${busy} data-run='["pki","interception"]'>Générer l'autorité d'interception</button></p></div>`}
  </section>`;

  h += `<section class="card"><div class="card-head"><h2>Certificats émis</h2><span class="meta">${p.certs.length} certificat(s)</span></div>
    <table><thead><tr><th>Nom</th><th>Noms (SAN)</th><th>Émetteur</th><th>Expire</th><th>Actions</th></tr></thead><tbody>
    ${p.certs.map(c => `<tr><td><strong>${esc(c.name)}</strong><div class="meta">${esc(kindLabel[c.kind] || c.kind)}${c.hasPfx ? " · .p12 dans pki/" : ""}</div></td>
      <td><div class="tags">${(c.sans || []).map(x => `<span class="pill mono">${esc(x)}</span>`).join("")}</div></td>
      <td class="meta">${esc(c.issuer)}</td><td>${expiry(c)}</td>
      <td><div class="actions">${files(c)}
        <select data-deploy-vm="${esc(c.name)}"><option value="">déployer sur…</option>${vmOpts}</select></div></td></tr>`).join("")
      || `<tr><td colspan="5" class="empty">Aucun certificat</td></tr>`}
    </tbody></table>
    <form class="inline" data-form="issue">
      <strong style="width:100%">Émettre un certificat</strong>
      <label>Nom<input name="name" required pattern="[A-Za-z0-9][A-Za-z0-9.-]*" placeholder="srv ou srv.pentest.${esc(p.domain)}"></label>
      <label>CA émettrice<select name="ca"><option value="">intermédiaire (tout ${esc(p.domain)})</option>
        ${subs.map(c => `<option value="${esc(c.name)}">${esc(c.name)} (${esc(c.domain || "")})</option>`).join("")}</select></label>
      <label>Noms DNS en plus<input name="dns" placeholder="www.x.${esc(p.domain)},*.apps.x.${esc(p.domain)}"></label>
      <label>Adresses IP<input name="ip" placeholder="10.66.66.40"></label>
      <label>Validité (jours)<input name="days" type="number" min="1" max="825" placeholder="825" style="width:90px"></label>
      <label class="check"><input type="checkbox" name="client"> client seulement</label>
      <label class="check"><input type="checkbox" name="pfx"> .p12</label>
      <button ${busy}>Émettre</button>
    </form>
  </section>`;

  h += `<section class="card"><div class="card-head"><h2>Maintenance de la chaîne</h2>
      <span class="meta">nouvelles clés ; certificats concernés réémis à l'identique ; ancienne PKI archivée avant toute opération</span></div>
    <table><tbody>
      <tr><td><strong>CA intermédiaire</strong><div class="meta">même racine : rien à faire sur les VMs</div></td>
        <td><button ${busy} data-run='["pki","renew","intermediate"]' data-confirm="Renouveler la CA intermédiaire et réémettre les certificats qu'elle a signés ?">Renouveler</button></td></tr>
      ${subs.map(c => `<tr><td><strong>CA dérivée ${esc(c.name)}</strong><div class="meta">mêmes limites (${esc(c.domain || "")}) : rien à faire sur les VMs</div></td>
        <td><button ${busy} data-run='${esc(JSON.stringify(["pki", "renew", "sub-ca", c.name]))}' data-confirm="Renouveler la CA dérivée ${esc(c.name)} et réémettre ses certificats ?">Renouveler</button></td></tr>`).join("")}
      <tr><td><strong>Toute la chaîne (nouvelle racine)</strong>
        <div class="meta">les VMs devront recevoir la nouvelle racine, et les certificats réémis être redéployés</div>
        <label class="check meta" style="display:flex;gap:6px;margin-top:6px"><input type="checkbox" id="rotate-trust" checked> installer ensuite la nouvelle racine sur les VMs démarrées</label></td>
        <td><button class="danger" ${busy} data-rotate-root>Régénérer…</button></td></tr>
    </tbody></table>
    <div class="meta" style="padding:10px 16px;border-top:1px solid var(--line)">Archives : ${(p.archives || []).length
      ? (p.archives || []).map(a => `<span class="pill mono">${esc(a)}</span>`).join(" ") + ` <span class="meta">dans pki/archive/ (retour arrière : recopier les fichiers)</span>`
      : "aucune"}</div>
  </section>`;

  const trustCell = (vm, val) => {
    const t = pkiTrust && pkiTrust[vm];
    if (!t) return `<span class="meta">?</span>`;
    if (!t.reachable) return `<span class="meta" title="${esc(t.err || 'injoignable')}">injoignable</span>`;
    return val ? `<span class="pill on">✓</span>` : `<span class="pill never">✗</span>`;
  };
  h += `<section class="card"><div class="card-head"><h2>Confiance dans les VMs</h2>
      <span class="meta">la racine est posée à la création des VMs ; « Vérifier » lit l'état réel par SSH (à la demande, pas au rafraîchissement)</span>
      <button style="margin-left:auto" ${running || trustChecking ? "disabled" : ""} data-check-trust>${trustChecking ? "Vérification…" : "Vérifier la confiance"}</button></div>
    <table><thead><tr><th>VM</th><th>CA racine</th><th>Interception</th><th>Installer</th></tr></thead><tbody>
    ${vms.map(vm => `<tr><td class="mono">${esc(vm)}</td>
      <td>${trustCell(vm, pkiTrust && pkiTrust[vm] && pkiTrust[vm].root)}</td>
      <td>${trustCell(vm, pkiTrust && pkiTrust[vm] && pkiTrust[vm].interception)}</td>
      <td><div class="actions">
        <button ${busy} data-trust='${esc(JSON.stringify(["pki", "trust-vm", vm]))}'>racine</button>
        <button ${busy} data-trust='${esc(JSON.stringify(["pki", "trust-vm", "--interception", vm]))}'>interception</button></div></td></tr>`).join("")
      || `<tr><td colspan="4" class="empty">Aucune VM</td></tr>`}
    </tbody></table>
    <div class="meta" style="padding:10px 16px;border-top:1px solid var(--line)">Racine : automatique à la création, utile ici pour une VM créée avant la PKI (sur ton poste : <span class="mono">labctl pki trust-host</span>, sudo). Interception : à poser seulement sur les VMs à observer — les autres doivent voir l'erreur de certificat.</div>
  </section>`;
  $("#view-pki").innerHTML = h;
}

// Formulaires de configuration (section dns du module.yaml)
document.addEventListener("submit", async e => {
  const f = e.target.closest("form[data-cfg-form]");
  if (!f) return;
  e.preventDefault();
  const fd = new FormData(f), v = Object.fromEntries(fd), module = f.dataset.module;
  const words = x => (x || "").split(/[\s,]+/).filter(Boolean);
  const kind = f.dataset.cfgForm;
  const body = kind === "host" ? { op: "host-add", module, name: v.name, octet: Number(v.octet), mac: v.mac }
    : kind === "conn" ? { op: "conn-add", module, target: v.target, from: fd.getAll("from"), ports: (v.ports || "").split(/[\s,]+/).filter(Boolean),
        via: v.via, local: !!v.local,
        proxy: v.via !== "proxy" ? null : { mode: v.pmode, allow: v.pmode === "filter" && v.plist === "allow" ? words(v.pdomains) : [],
          deny: v.pmode === "filter" && v.plist === "deny" ? words(v.pdomains) : [], ports: words(v.pports) },
        portal: v.via !== "portal" ? null : { auth: v.auth, password: v.auth === "password" ? v.password : "", duration: v.duration,
          title: v.title, detect: !!v.detect, api: !!v.api, https: v.https } }
    : { op: "alias-add", module, name: v.name, target: v.target };
  if (await editConfig(body) && kind === "conn") toast("Connexion ajoutée : « Appliquer sur la passerelle » pour l'activer");
});

// Formulaires PKI -> arguments de labctl pki
document.addEventListener("submit", e => {
  const f = e.target.closest("form[data-form]");
  if (!f) return;
  e.preventDefault();
  const v = Object.fromEntries(new FormData(f));
  const csv = s => (s || "").split(/[\s,]+/).filter(Boolean).join(",");
  if (f.dataset.form === "subca") {
    const args = ["pki", "sub-ca", v.name, "-D", v.domain.trim()];
    if (v.subnet.trim()) args.push("-s", v.subnet.trim());
    run(args, `Créer la CA dérivée « ${v.name} », limitée à ${v.domain}${v.subnet ? " et " + v.subnet : ""} ?`);
  } else if (f.dataset.form === "issue") {
    const args = ["pki", "issue", v.name.trim()];
    if (v.ca) args.push("-C", v.ca);
    if (csv(v.dns)) args.push("-d", csv(v.dns));
    if (csv(v.ip)) args.push("-i", csv(v.ip));
    if (v.days) args.push("--days", v.days);
    if (v.client) args.push("--client");
    if (v.pfx) args.push("--pfx");
    run(args);
  }
});

// Page PKI : vérifier la confiance (sonde SSH, à la demande) et poser une CA dans une VM.
document.addEventListener("click", e => {
  if (e.target.closest("[data-check-trust]")) { checkTrust(); return; }
  const b = e.target.closest("[data-trust]");
  if (!b) return;
  const args = JSON.parse(b.dataset.trust), vm = args[args.length - 1];
  const which = args.includes("--interception") ? "l'autorité d'interception" : "la CA racine";
  run(args, `Installer ${which} dans ${vm} (par SSH) ?`).then(ok => { if (ok) checkTrust(); });
});
document.addEventListener("change", e => {
  const s = e.target.closest("select[data-deploy-vm]");
  if (!s || !s.value) return;
  const vm = s.value; s.value = "";
  run(["pki", "deploy", vm, s.dataset.deployVm], `Copier la clé, le certificat et la chaîne « ${s.dataset.deployVm} » dans ${vm}:/etc/ssl/lab ?`);
});
