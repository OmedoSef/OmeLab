// GUI de labctl — utilitaires de base : jeton, sélection, échappement HTML, appels à l'API, notifications,
// confirmations, téléchargements. Module ES sans aucun import : toujours évalué en premier.
export const token = new URLSearchParams(location.hash.slice(1)).get("t") || "";

export const $ = (sel, el = document) => el.querySelector(sel);

export const esc = s => String(s ?? "").replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

export const stateLabel = { running: "démarrée", "shut off": "arrêtée", paused: "en pause", absente: "absente", crashed: "plantée", pmsuspended: "suspendue", "in shutdown": "en arrêt" };

export const stLabel = s => stateLabel[s] || s;

// Notification en bas à droite ; les erreurs restent jusqu'à fermeture
export function toast(msg, err = false, withLog = false) {
  const t = document.createElement("div");
  t.className = "toast" + (err ? " err" : "");
  t.innerHTML = `<span>${err ? "✗" : "✓"}</span><span class="msg">${esc(msg)}</span>${withLog ? `<button data-show-log>Console</button>` : ""}<button data-close aria-label="Fermer">×</button>`;
  t.addEventListener("click", e => {
    if (e.target.dataset.showLog !== undefined) openLog();
    if (e.target.closest("button")) t.remove();
  });
  $("#toasts").append(t);
  if (!err) setTimeout(() => t.remove(), 6000);
}

// Boîte de dialogue : confirmation (true), saisie ou choix (valeur), annulation (null)
export function ask({ title, text = "", ok = "Confirmer", danger = false, input = false, options = null, value = "", pattern = "", hint = "", cancel = true }) {
  const d = $("#dlg");
  const field = options
    ? `<select name="v">${options.map(x => `<option ${x === value ? "selected" : ""}>${esc(x)}</option>`).join("")}</select>`
    : input ? `<input name="v" value="${esc(value)}" required autocomplete="off" spellcheck="false" ${pattern ? `pattern="${esc(pattern)}" title="${esc(hint)}"` : ""}>` : "";
  // Bouton de validation en premier : c'est lui qu'Entrée déclenche (l'ordre affiché est inversé en CSS)
  d.innerHTML = `<form method="dialog"><h3>${esc(title)}</h3>${text ? `<p>${esc(text)}</p>` : ""}${field}
    <div class="dlg-actions"><button value="ok" class="${danger ? "danger-solid" : "primary"}">${esc(ok)}</button>
    ${cancel ? `<button value="" formnovalidate>Annuler</button>` : ""}</div></form>`;
  return new Promise(res => {
    // Réponse à la validation du formulaire (immédiate), sinon à la fermeture (Échap) ; une seule fois
    let done = false;
    const finish = ok => {
      if (done) return;
      done = true;
      const f = d.querySelector("[name=v]");
      res(ok ? (f ? f.value.trim() : true) : null);
    };
    d.querySelector("form").addEventListener("submit", e => finish(e.submitter?.value === "ok"));
    d.onclose = () => finish(d.returnValue === "ok");
    d.returnValue = "";
    d.showModal();
    const f = d.querySelector("[name=v]");
    if (f) { f.focus(); f.select?.(); } else d.querySelector("button[value=ok]").focus();
  });
}

export function openLog() { $("#log").classList.remove("collapsed"); $("#log-toggle").textContent = "Masquer"; }

export async function api(path, opts = {}) {
  const r = await fetch(path, { ...opts, headers: { "X-Lab-Token": token, "Content-Type": "application/json", ...(opts.headers || {}) } });
  if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
  return r;
}

export const lastOctet = ip => { const m = /(\d+)\s*$/.exec(ip || ""); return m ? parseInt(m[1], 10) : 0; };

export async function download(path) {
  try {
    const blob = await (await api("/api/pki/file?path=" + encodeURIComponent(path))).blob();
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob); a.download = path.split("/").pop();
    document.body.append(a); a.click(); a.remove(); URL.revokeObjectURL(a.href);
  } catch (e) { toast(e.message, true); }
}
