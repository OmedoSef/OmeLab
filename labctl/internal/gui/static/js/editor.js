// GUI de labctl — éditeur de module.yaml. Scripts classiques chargés dans l'ordre par index.html (portée globale
// partagée) : une fonction n'est utilisable qu'après le chargement du fichier qui la définit.
"use strict";

// --- Éditeur de module.yaml : coloration, numéros de ligne, validation à la volée
const ed = { want: "", modules: [], name: "", text: "", saved: "", hash: "", create: false, issues: [], timer: 0, seq: 0, msg: "" };
const dirty = () => ed.name && ed.text !== ed.saved;
addEventListener("beforeunload", e => { if (dirty() || fdirty()) e.preventDefault(); });
const discard = text => ask({ title: "Modifications non enregistrées", text, ok: "Abandonner les modifications", danger: true });

// ed.want : module à ouvrir (page m/<module>/yaml)
async function openEditor() {
  const name = ed.want; ed.want = "";
  try { ed.modules = (await (await api("/api/modules")).json()).modules; }
  catch (e) { $("#view-editor").innerHTML = `<div class="empty err">${esc(e.message)}</div>`; return; }
  if (name && name !== ed.name && !(await loadModuleText(name))) {
    // modifications d'un autre module gardées : on reste dessus
    if (ed.name) go(ed.create ? "new" : `m/${ed.name}/yaml`);
    return;
  }
  renderEditor();
}

async function loadModuleText(name) {
  if (dirty() && !(await discard(`Les modifications de ${ed.name} seront perdues.`))) return false;
  try {
    const d = await (await api("/api/module?name=" + encodeURIComponent(name))).json();
    Object.assign(ed, { name, text: d.text, saved: d.text, hash: d.hash, create: false, issues: d.issues, msg: "" });
    return true;
  } catch (e) { toast(e.message, true); return false; }
}

// Relit le fichier sur le disque (abandonne les modifications, après confirmation)
async function reloadModule() {
  if (dirty() && !(await discard(`Relire modules/${ed.name}/module.yaml depuis le disque : les modifications seront perdues.`))) return;
  const name = ed.name;
  ed.saved = ed.text; // la question vient d'être posée
  if (await loadModuleText(name)) renderEditor();
}

async function newModule() {
  if (dirty() && !(await discard(`Les modifications de ${ed.name} seront perdues.`))) return false;
  const name = await ask({ title: "Nouveau module", text: "Nom : minuscules, chiffres et tirets, 9 caractères au plus.\nIl donne aussi le nom des VMs (<module>-<vm>).",
    input: true, pattern: "[a-z0-9]([a-z0-9-]{0,7}[a-z0-9])?", hint: "minuscules, chiffres, tirets ; 9 caractères au plus", ok: "Créer le brouillon" });
  if (!name) return false;
  if (ed.modules.includes(name)) { toast(`Le module ${name} existe déjà.`, true); return false; }
  let d;
  try { d = await (await api("/api/module/skeleton?name=" + encodeURIComponent(name))).json(); }
  catch (e) { toast(e.message, true); return false; }
  Object.assign(ed, { name, text: d.text, saved: "", hash: "", create: true, issues: d.issues, msg: "nouveau module : enregistrer pour créer modules/" + name + "/" });
  renderEditor();
  go("new");
  return true;
}

// Coloration d'une ligne YAML (clés, chaînes, nombres, variables ${...}, commentaires)
function hlLine(line) {
  let code = line, comment = "", q = "";
  for (let i = 0; i < line.length; i++) {
    const c = line[i];
    if (q) { if (c === q) q = ""; continue; }
    if (c === '"' || c === "'") q = c;
    else if (c === "#" && (i === 0 || /\s/.test(line[i - 1]))) { code = line.slice(0, i); comment = line.slice(i); break; }
  }
  const re = /(\$\{[^}]*\})|("(?:[^"\\]|\\.)*"|'[^']*')|([A-Za-z0-9_.-]+)(?=\s*:(?:\s|$))|((?<=(?:^|:\s|[\[{,]|^\s*-\s)\s*)(?:-?\d+(?:\.\d+)?|true|false|null)(?=\s*(?:$|[,\]}])))|([{}\[\],]|^\s*-(?=\s))/g;
  let out = "", last = 0, m;
  while ((m = re.exec(code))) {
    out += esc(code.slice(last, m.index));
    const cls = m[1] ? "y-v" : m[2] ? "y-s" : m[3] ? "y-k" : m[4] ? "y-n" : "y-p";
    // une variable dans une chaîne reste visible
    out += m[2] ? `<span class="y-s">${esc(m[2]).replace(/\$\{[^}]*\}/g, v => `<span class="y-v">${v}</span>`)}</span>` : `<span class="${cls}">${esc(m[0])}</span>`;
    last = re.lastIndex;
    if (m[0] === "") re.lastIndex++;
  }
  out += esc(code.slice(last));
  if (comment) out += `<span class="y-c">${esc(comment)}</span>`;
  return out;
}

function lineLevels() {
  const lv = {};
  for (const i of ed.issues) if (i.line > 0) lv[i.line] = i.error ? "e" : (lv[i.line] || "w");
  return lv;
}

function paintEditor() {
  const ta = $("#ed-text");
  if (!ta) return;
  const lines = ed.text.split("\n"), lv = lineLevels();
  const cur = ed.text.slice(0, ta.selectionStart).split("\n").length;
  $("#ed-hl").innerHTML = lines.map((l, i) => `<span class="ln ${lv[i + 1] || ""} ${i + 1 === cur ? "cur" : ""}">${hlLine(l) || " "}</span>`).join("") + `<span class="ln"> </span>`;
  $("#ed-gutter").innerHTML = lines.map((_, i) => `<div class="${lv[i + 1] || ""}">${i + 1}</div>`).join("") + "<div></div>";
  syncScroll();
  const errs = ed.issues.filter(i => i.error).length, warns = ed.issues.length - errs;
  $("#ed-issues").innerHTML = ed.issues.length
    ? ed.issues.map(i => `<li class="${i.error ? "e" : "w"}" data-goto="${i.line || 0}"><span class="lvl">${i.error ? "erreur" : "avert."}</span>
        <span class="meta mono">${i.line ? "ligne " + i.line : ""}${i.path ? " " + esc(i.path) : ""}</span><span>${esc(i.msg)}</span></li>`).join("")
    : `<li class="ok">Aucun problème : le module est valide.</li>`;
  const st = $("#ed-state");
  st.innerHTML = (errs ? `<span class="err">${errs} erreur(s)</span>` : `<span class="pill on">valide</span>`) +
    (warns ? ` <span class="warn">${warns} avertissement(s)</span>` : "") +
    (dirty() ? ` · <strong>modifié</strong>` : ed.create ? "" : ` · enregistré`) + (ed.msg ? ` · <span class="meta">${esc(ed.msg)}</span>` : "");
  $("#ed-save").disabled = running || !!errs || !dirty();
  $("#ed-revert").disabled = running || ed.create;
  $("#ed-apply").disabled = running || dirty() || ed.create || !!errs;
}

function syncScroll() {
  const ta = $("#ed-text");
  $("#ed-hl").scrollTop = ta.scrollTop; $("#ed-hl").scrollLeft = ta.scrollLeft;
  $("#ed-gutter").scrollTop = ta.scrollTop;
}

function renderEditor() {
  $("#view-editor").innerHTML = `<section class="card">
    <div class="ed-bar">
      <button id="ed-save" class="primary" title="Ctrl+S">${ed.create ? "Créer le module" : "Enregistrer"}</button>
      <button id="ed-revert" title="Abandonne les modifications et relit le fichier">Recharger</button>
      <button id="ed-apply" title="labctl ${esc(ed.name)} up : crée ce qui manque (réseau, CA, VMs)">Appliquer (up)</button>
      <span class="spacer"></span>
      <span id="ed-state" class="state"></span>
    </div>
    <div class="editor">
      <div class="gutter" id="ed-gutter"></div>
      <div class="area"><pre id="ed-hl" aria-hidden="true"></pre><textarea id="ed-text" spellcheck="false" autocapitalize="off" autocomplete="off"></textarea></div>
    </div>
    <ul class="issues" id="ed-issues"></ul>
    <div class="ed-bar" style="border-top:1px solid var(--line);border-bottom:0">
      <span class="meta mono">modules/${esc(ed.name)}/module.yaml</span>
      <span class="spacer"></span>
      <a class="meta" href="#" id="ed-help">Aide : clés et variables</a>
    </div>
  </section>`;
  const ta = $("#ed-text");
  ta.value = ed.text;
  paintEditor();
}

function scheduleCheck() {
  clearTimeout(ed.timer);
  ed.timer = setTimeout(async () => {
    const seq = ++ed.seq;
    try {
      const d = await (await api("/api/module/check", { method: "POST", body: JSON.stringify({ name: ed.name, text: ed.text }) })).json();
      if (seq === ed.seq) { ed.issues = d.issues; paintEditor(); }
    } catch (e) { ed.msg = e.message; paintEditor(); }
  }, 350);
}

async function saveModule() {
  if (!dirty() || ed.issues.some(i => i.error)) return;
  const r = await fetch("/api/module/save", { method: "POST", headers: { "X-Lab-Token": token, "Content-Type": "application/json" },
    body: JSON.stringify({ name: ed.name, text: ed.text, hash: ed.hash, create: ed.create }) });
  const d = await r.json().catch(() => ({ error: r.statusText }));
  if (!r.ok) {
    if (d.issues) ed.issues = d.issues;
    ed.msg = d.error || "erreur";
    paintEditor();
    toast(d.error || "enregistrement refusé", true);
    return;
  }
  const created = ed.create;
  Object.assign(ed, { saved: ed.text.endsWith("\n") ? ed.text : ed.text + "\n", hash: d.hash, issues: d.issues, create: false,
    msg: created ? "module créé : « Appliquer » pour le construire" : "" });
  if (!ed.text.endsWith("\n")) ed.text += "\n";
  if (created) { ed.modules.push(ed.name); renderEditor(); } else paintEditor();
  await load(true); loadConfig();
  if (created) go(`m/${ed.name}/yaml`); else render(); // pastille « modifié » de l'onglet
}

// Un « up » peut réécrire le fichier (sous-réseau, MAC) : on le relit ensuite
async function applyModule() {
  const name = ed.name;
  await run([name, "up"], `Construire le module ${name} (labctl ${name} up) : réseau, CA, VMs manquantes ? Les VMs existantes ne sont pas recréées.`);
  const saved = ed.saved;
  ed.saved = ed.text; // pas de question « abandonner » : rien n'est modifié
  if (!(await loadModuleText(name))) ed.saved = saved;
  else { ed.msg = ed.text !== saved ? "fichier complété par up (sous-réseau, MAC)" : ""; }
  renderEditor();
}

document.addEventListener("input", e => {
  if (e.target.id !== "ed-text") return;
  const was = dirty();
  ed.text = e.target.value; ed.msg = "";
  paintEditor(); scheduleCheck();
  if (was !== dirty()) render(); // pastille « modifié » de l'onglet
});
document.addEventListener("scroll", e => { if (e.target.id === "ed-text") syncScroll(); }, true);
document.addEventListener("selectionchange", () => { if (document.activeElement?.id === "ed-text") paintEditor(); });
document.addEventListener("keydown", e => {
  const ta = e.target;
  if (ta.id !== "ed-text") return;
  if ((e.ctrlKey || e.metaKey) && e.key === "s") { e.preventDefault(); saveModule(); return; }
  if (e.key === "Tab" && !e.shiftKey) { e.preventDefault(); document.execCommand("insertText", false, "  "); return; }
  if (e.key === "Enter") { // garde l'indentation de la ligne courante (et l'augmente après « clé: »)
    const before = ta.value.slice(0, ta.selectionStart), line = before.slice(before.lastIndexOf("\n") + 1);
    let ind = line.match(/^\s*(- )?/)[0].replace("- ", "  ");
    if (/:\s*$/.test(line)) ind += "  ";
    e.preventDefault(); document.execCommand("insertText", false, "\n" + ind);
  }
});
document.addEventListener("click", e => {
  const t = e.target.closest("[data-goto], #ed-save, #ed-revert, #ed-apply, #ed-help, [data-new-module]");
  if (!t) return;
  if (t.dataset.newModule !== undefined) newModule();
  else if (t.id === "ed-save") saveModule();
  else if (t.id === "ed-revert") reloadModule();
  else if (t.id === "ed-apply") applyModule();
  else if (t.id === "ed-help") { e.preventDefault(); ask({ title: "Aide module.yaml", cancel: false, ok: "Fermer", text: "Référence complète des clés : docs/module-yaml.md (dans le dépôt Labs).\n\nVariables : ${module.name} ${module.domain} ${network.subnet} ${network.gateway} ${vm.name} ${vm.ip} ${vm.fqdn} ${vms.<vm>.ip} ${vms.<vm>.fqdn} ${keyboard}" }); }
  else if (t.dataset.goto !== undefined) {
    const n = +t.dataset.goto, ta = $("#ed-text");
    if (!n || !ta) return;
    const lines = ed.text.split("\n"), start = lines.slice(0, n - 1).join("\n").length + (n > 1 ? 1 : 0);
    ta.focus(); ta.setSelectionRange(start, start + (lines[n - 1] || "").length);
    ta.scrollTop = Math.max(0, (n - 5) * parseFloat(getComputedStyle($(".editor")).getPropertyValue("--lh")));
    syncScroll(); paintEditor();
  }
});
