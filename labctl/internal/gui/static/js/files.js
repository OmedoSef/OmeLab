// GUI de labctl — onglet Fichiers d'un module. Module ES : ce qu'il partage est exporté, ce qu'il utilise est importé.
import { $, api, ask, esc, go, modPage, state, toast } from "./core.js";
import { doing } from "./run.js";
import { render } from "./render.js";
import { tr } from "./traffic.js";
import { files } from "./pki.js";
import { ed } from "./editor.js";

// --- Onglet Fichiers : modules/<m>/files/, copiés dans les VMs par files: du module.yaml
// fx.open : fichier ouvert dans l'éditeur texte (create : pas encore enregistré)
export const fx = { module: "", list: null, dir: "", open: null, busy: "" };
export const fdirty = () => !!fx.open && (fx.open.create || fx.open.text !== fx.open.saved);
export const fpath = p => `files/${p}`;
export const fsize = n => n < 1024 ? `${n} o` : n < 1 << 20 ? `${(n / 1024).toFixed(1)} Kio` : `${(n / (1 << 20)).toFixed(1)} Mio`;
export const fq = (path, extra = "") => `module=${encodeURIComponent(fx.module)}&path=${encodeURIComponent(path)}${extra}`;

export async function loadFiles(name) {
  if (name !== fx.module && fdirty()) {
    if (!(await ask({ title: "Fichier non enregistré", text: `Les modifications de ${fpath(fx.open.path)} (${fx.module}) seront perdues.`, ok: "Abandonner les modifications", danger: true }))) {
      go(`m/${fx.module}/files`); return;
    }
  }
  if (name !== fx.module) Object.assign(fx, { module: name, dir: "", open: null });
  try { fx.list = await (await api(`/api/files?module=${encodeURIComponent(name)}`)).json(); }
  catch (e) { $("#mod-files").innerHTML = `<div class="empty err">${esc(e.message)}</div>`; return; }
  renderFiles();
}

export function renderFiles() {
  const [name, sub] = modPage();
  if (sub !== "files" || !fx.list || fx.module !== name) return;
  const L = fx.list, files = L.files || [], dirs = files.filter(f => f.dir).map(f => f.path);
  if (fx.dir && !dirs.includes(fx.dir)) fx.dir = "";
  const ref = u => `<span class="pill mono" title="${u.template ? "template : ${…} remplacés à la copie" : "copié tel quel"}">${esc(u.vm)} : ${esc(u.dest)}${u.template ? " ⟨t⟩" : ""}</span>`;
  let h = `<section class="card" id="f-card">
    <div class="ed-bar">
      <input type="file" id="f-pick" multiple hidden>
      <button class="primary" id="f-upload">Envoyer des fichiers…</button>
      <label class="meta">dans <select id="f-dir"><option value="">files/</option>${dirs.map(d => `<option ${d === fx.dir ? "selected" : ""} value="${esc(d)}">files/${esc(d)}/</option>`).join("")}</select></label>
      <button id="f-newfile">Nouveau fichier…</button>
      <button id="f-newdir">Nouveau dossier…</button>
      <span class="spacer"></span>
      ${fx.busy ? `<span class="doing"><span class="spin"></span>${esc(fx.busy)}</span>` : `<span class="meta">ou glisser-déposer des fichiers ici</span>`}
    </div>
    <div class="card-desc meta">Les fichiers sont copiés dans une VM à sa création (<span class="mono">files:</span> du module.yaml) :
      une VM existante ne reçoit pas les changements d'elle-même. ⟨t⟩ : template, les <span class="mono">\${…}</span> sont remplacés.</div>
    ${L.warning ? `<div class="banner" style="border-radius:0;border-width:0 0 1px"><span>${esc(L.warning)}</span></div>` : ""}
    <table class="ftree"><thead><tr><th>Nom</th><th>Taille</th><th>Modifié</th><th>Copié dans (VM : destination)</th><th></th></tr></thead><tbody>
    ${files.map(f => {
      const depth = f.path.split("/").length - 1, base = f.path.split("/").pop();
      const act = f.dir ? "" : `${f.text ? `<button data-fedit="${esc(f.path)}">Éditer</button>` : ""}<button data-fget="${esc(f.path)}" title="Télécharger">⤓</button>`;
      return `<tr><td style="padding-left:${16 + depth * 20}px"><span class="ico">${f.dir ? "▸" : "·"}</span>${f.dir ? `<strong>${esc(base)}/</strong>`
          : f.text ? `<button class="linkbtn" style="font-weight:400" data-fedit="${esc(f.path)}">${esc(base)}</button>` : esc(base)}</td>
        <td class="meta">${f.dir ? "" : fsize(f.size)}</td>
        <td class="meta">${new Date(f.mod).toLocaleString("fr-FR", { dateStyle: "short", timeStyle: "short" })}</td>
        <td><div class="tags">${f.dir ? "" : (f.uses || []).map(ref).join("") || `<span class="meta">non utilisé</span>`}</div></td>
        <td><div class="actions" style="justify-content:flex-end">${act}
          <button data-fmv="${esc(f.path)}">Renommer…</button>
          <button class="danger" data-frm='${esc(JSON.stringify([f.path, f.dir, (f.uses || []).length]))}'>Supprimer</button></div></td></tr>`;
    }).join("") || `<tr><td colspan="5" class="empty">Aucun fichier.</td></tr>`}
    </tbody></table>
    ${(L.missing || []).length ? `<div class="card-head" style="border-top:1px solid var(--line);border-bottom:0;display:block">
      <strong class="err">Référencés par module.yaml mais absents</strong>
      ${L.missing.map(u => `<div style="display:flex;gap:10px;align-items:center;margin-top:6px"><span class="mono">${esc(u.src)}</span> ${ref(u)}
        <span class="spacer"></span><button data-fcreate="${esc(u.src.replace(/^files\//, ""))}">Créer (texte)</button></div>`).join("")}</div>` : ""}
    ${(L.outside || []).length ? `<div class="card-desc meta" style="border-top:1px solid var(--line)">Hors de files/ (non gérés ici) :
      ${L.outside.map(u => `<span class="mono">${esc(u.src)}</span>`).join(", ")}</div>` : ""}
  </section>`;

  const o = fx.open;
  if (o) h += `<section class="card"><div class="ed-bar">
      <strong class="mono">${esc(fpath(o.path))}</strong>
      <span id="f-state" class="state"></span>
      <span class="spacer"></span>
      <button class="primary" id="f-save" title="Ctrl+S">${o.create ? "Créer" : "Enregistrer"}</button>
      <button id="f-close">Fermer</button>
    </div><textarea class="ftext" id="f-text" spellcheck="false" autocapitalize="off" autocomplete="off"></textarea></section>`;
  $("#mod-files").innerHTML = h;
  if (o) { $("#f-text").value = o.text; paintFileState(); }
}

export function paintFileState() {
  const st = $("#f-state");
  if (!st || !fx.open) return;
  st.innerHTML = fx.open.create ? `<span class="pill">nouveau</span>` : fdirty() ? "<strong>modifié</strong>" : `<span class="meta">enregistré</span>`;
  $("#f-save").disabled = !fdirty();
}

export async function openFile(path, create = false) {
  if (fx.open && fdirty() && fx.open.path !== path &&
      !(await ask({ title: "Fichier non enregistré", text: `Les modifications de ${fpath(fx.open.path)} seront perdues.`, ok: "Abandonner les modifications", danger: true }))) return;
  if (create) fx.open = { path, text: "", saved: "", hash: "", create: true };
  else {
    try {
      const d = await (await api(`/api/file?${fq(path)}`)).json();
      fx.open = { path, text: d.text, saved: d.text, hash: d.hash, create: false };
    } catch (e) { toast(e.message, true); return; }
  }
  renderFiles(); render();
  $("#f-text")?.focus();
  $("#f-text")?.scrollIntoView({ block: "nearest", behavior: "smooth" });
}

export async function saveFile() {
  const o = fx.open;
  if (!o || !fdirty()) return;
  try {
    const d = await (await api("/api/file/save", { method: "POST", body: JSON.stringify({ module: fx.module, path: o.path, text: o.text, hash: o.hash, create: o.create }) })).json();
    Object.assign(o, { saved: o.text, hash: d.hash, create: false });
    toast(`${fpath(o.path)} enregistré`);
  } catch (e) { toast(e.message, true); return; }
  await loadFiles(fx.module); render();
}

export async function closeFile() {
  if (fdirty() && !(await ask({ title: "Fichier non enregistré", text: `Les modifications de ${fpath(fx.open.path)} seront perdues.`, ok: "Fermer sans enregistrer", danger: true }))) return;
  fx.open = null; renderFiles(); render();
}

// Envoi : un fichier après l'autre ; ceux qui existent déjà ne sont remplacés qu'après confirmation
export async function uploadFiles(list) {
  const items = [...list].filter(f => f.size > 0 || f.type !== ""); // un dossier glissé arrive comme un fichier vide sans type
  if (!items.length) { toast("Rien à envoyer (les dossiers ne sont pas pris : envoyer leurs fichiers)", true); return; }
  const big = items.find(f => f.size > 512 * 1024 * 1024);
  if (big) { toast(`${big.name} : plus de 512 Mio`, true); return; }
  const target = f => (fx.dir ? fx.dir + "/" : "") + f.name;
  const existing = new Set((fx.list?.files || []).filter(f => !f.dir).map(f => f.path));
  const clash = items.filter(f => existing.has(target(f)));
  if (clash.length && !(await ask({ title: "Remplacer ?", danger: true, ok: "Remplacer",
      text: `Déjà dans files/ : ${clash.map(target).join(", ")}` }))) return;
  let n = 0;
  for (const f of items) {
    fx.busy = `envoi de ${f.name} (${fsize(f.size)})…`; renderFiles();
    try {
      await api(`/api/file/upload?${fq(target(f), clash.includes(f) ? "&overwrite=1" : "")}`,
        { method: "POST", body: f, headers: { "Content-Type": "application/octet-stream" } });
      n++;
    } catch (e) { toast(`${f.name} : ${e.message}`, true); }
  }
  fx.busy = "";
  if (n) toast(`${n} fichier(s) envoyé(s) dans files/${fx.dir ? fx.dir + "/" : ""}`);
  await loadFiles(fx.module);
}

export async function fileOp(body, okMsg) {
  let ok = false;
  try { await api("/api/file/op", { method: "POST", body: JSON.stringify({ module: fx.module, ...body }) }); toast(okMsg); ok = true; }
  catch (e) { toast(e.message, true); }
  await loadFiles(fx.module);
  return ok;
}

document.addEventListener("click", async e => {
  const t = e.target.closest("#f-upload, #f-newfile, #f-newdir, #f-save, #f-close, [data-fedit], [data-fget], [data-fmv], [data-frm], [data-fcreate]");
  if (!t) return;
  const prefix = fx.dir ? fx.dir + "/" : "";
  const relPattern = "(?!/).+", relHint = "chemin dans files/, ex. conf/app.ini (sans .. ni / au début)";
  if (t.id === "f-upload") $("#f-pick").click();
  else if (t.id === "f-newfile") {
    const p = await ask({ title: "Nouveau fichier texte", text: "Chemin dans files/ (les dossiers manquants sont créés) :", input: true, value: prefix, pattern: relPattern, hint: relHint, ok: "Créer" });
    if (p) openFile(p.replace(/^\/+/, ""), true);
  } else if (t.id === "f-newdir") {
    const p = await ask({ title: "Nouveau dossier", text: "Chemin dans files/ :", input: true, value: prefix, pattern: relPattern, hint: relHint, ok: "Créer" });
    if (p) fileOp({ op: "mkdir", path: p }, `dossier files/${p} créé`);
  } else if (t.id === "f-save") saveFile();
  else if (t.id === "f-close") closeFile();
  else if (t.dataset.fedit) openFile(t.dataset.fedit);
  else if (t.dataset.fcreate) openFile(t.dataset.fcreate, true);
  else if (t.dataset.fget) {
    try {
      const blob = await (await api(`/api/file/raw?${fq(t.dataset.fget)}`)).blob();
      const a = document.createElement("a");
      a.href = URL.createObjectURL(blob); a.download = t.dataset.fget.split("/").pop();
      document.body.append(a); a.click(); a.remove(); URL.revokeObjectURL(a.href);
    } catch (err) { toast(err.message, true); }
  } else if (t.dataset.fmv) {
    const from = t.dataset.fmv;
    const to = await ask({ title: `Renommer files/${from}`, text: "Nouveau chemin dans files/ (un autre dossier : déplacement).\nPenser à mettre à jour les src: du module.yaml qui le citent.",
      input: true, value: from, pattern: relPattern, hint: relHint, ok: "Renommer" });
    if (to && to !== from) {
      if (!(await fileOp({ op: "rename", path: from, to }, `files/${from} → files/${to}`))) return;
      // le fichier ouvert suit (lui-même, ou le dossier qui le contient)
      if (fx.open && (fx.open.path === from || fx.open.path.startsWith(from + "/"))) { fx.open.path = to + fx.open.path.slice(from.length); renderFiles(); }
    }
  } else if (t.dataset.frm) {
    const [path, dir, used] = JSON.parse(t.dataset.frm);
    const ok = await ask({ title: `Supprimer files/${path}${dir ? "/" : ""} ?`, danger: true, ok: "Supprimer",
      text: (dir ? "Le dossier et tout son contenu seront supprimés." : "Le fichier sera supprimé.") +
        (used ? "\nIl est utilisé par le module.yaml : le module deviendra invalide tant que les src: ne seront pas corrigés." : "") });
    if (ok) {
      if (await fileOp({ op: "delete", path }, `files/${path} supprimé`) && fx.open && (fx.open.path === path || fx.open.path.startsWith(path + "/"))) { fx.open = null; renderFiles(); render(); }
    }
  }
});
document.addEventListener("change", e => {
  const f = e.target.closest("form[data-cfg-form=conn]");
  if (f && (e.target.dataset.via !== undefined || e.target.dataset.auth !== undefined || e.target.dataset.pmode !== undefined)) {
    $(".portal-opts", f).hidden = f.via.value !== "portal";
    $(".proxy-opts", f).hidden = f.via.value !== "proxy";
    f.querySelectorAll(".pxf").forEach(x => { x.hidden = f.pmode.value !== "filter"; });
    f.pdomains.required = f.via.value === "proxy" && f.pmode.value === "filter";
    $(".pw", f).hidden = f.auth.value !== "password";
    f.password.required = f.via.value === "portal" && f.auth.value === "password";
  }
});
document.addEventListener("change", e => {
  if (e.target.id === "f-pick") { uploadFiles(e.target.files); e.target.value = ""; }
  else if (e.target.id === "f-dir") fx.dir = e.target.value;
});
document.addEventListener("input", e => {
  if (e.target.id !== "f-text" || !fx.open) return;
  const was = fdirty();
  fx.open.text = e.target.value; paintFileState();
  if (was !== fdirty()) render();
});
document.addEventListener("keydown", e => {
  if (e.target.id !== "f-text") return;
  if ((e.ctrlKey || e.metaKey) && e.key === "s") { e.preventDefault(); saveFile(); }
  else if (e.key === "Tab" && !e.shiftKey) { e.preventDefault(); document.execCommand("insertText", false, "  "); }
});
// Glisser-déposer sur la carte des fichiers
for (const ev of ["dragenter", "dragover"]) document.addEventListener(ev, e => {
  const c = e.target.closest?.("#f-card");
  if (c && e.dataTransfer?.types.includes("Files")) { e.preventDefault(); c.classList.add("drop"); }
});
document.addEventListener("dragleave", e => { const c = e.target.closest?.("#f-card"); if (c && !c.contains(e.relatedTarget)) c.classList.remove("drop"); });
document.addEventListener("drop", e => {
  const c = e.target.closest?.("#f-card");
  if (!c) return;
  e.preventDefault(); c.classList.remove("drop");
  if (e.dataTransfer.files.length) uploadFiles(e.dataTransfer.files);
});
