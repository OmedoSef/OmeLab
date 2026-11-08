// GUI de labctl — actions (délégation d'événements).
// Module ES : ce qu'il partage est exporté, ce qu'il utilise est importé.
import { $, ask, download } from "./util.js";
import { attachPicker, detachVM, editConfig, go, load } from "./core.js";
import { console_, run } from "./run.js";
import { openPending } from "./pending.js";

// --- Actions (délégation d'événements)
document.addEventListener("click", e => {
  const t = e.target.closest("button, input[type=checkbox]");
  if (!t) return;
  if (t.dataset.go) go(t.dataset.go);
  else if (t.dataset.download) download(t.dataset.download);
  else if (t.dataset.cfg) { e.preventDefault(); editConfig(JSON.parse(t.dataset.cfg)); }
  else if (t.dataset.detach) { e.preventDefault(); detachVM(JSON.parse(t.dataset.detach)); }
  else if (t.dataset.attachAdd) { e.preventDefault(); attachPicker(t.dataset.attachAdd); }
  else if (t.id === "pending-pill") openPending();
  else if (t.dataset.closePending !== undefined) $("#pending-dlg").close();
  else if (t.dataset.applyPending !== undefined) { $("#pending-dlg").close(); run(["core", "apply"]); }
  else if (t.dataset.rotateRoot !== undefined) rotateRoot();
  else if (t.dataset.run) run(JSON.parse(t.dataset.run), t.dataset.confirm, t.classList.contains("danger"));
  else if (t.dataset.console) console_(t.dataset.console);
  else if (t.dataset.snapshot) snapshotVM(...JSON.parse(t.dataset.snapshot));
  else if (t.dataset.restore) restoreVM(...JSON.parse(t.dataset.restore));
  else if (t.dataset.link) {
    e.preventDefault();
    const [op, a, b] = JSON.parse(t.dataset.link);
    run(["core", op, a, b], op === "link" ? `Autoriser ${a} à joindre et résoudre ${b} ?` : `Retirer le lien ${a} → ${b} ?`);
  } else if (t.dataset.cmdRun) {
    const input = $(`[data-cmd="${CSS.escape(t.dataset.cmdRun)}"]`);
    const words = input.value.trim().split(/\s+/).filter(Boolean);
    if (!words.length) return;
    const destructive = words[0] === "destroy";
    run([t.dataset.cmdRun, ...words], destructive ? `« ${words.join(" ")} » supprime définitivement des VMs.` : null, destructive)
      .then(done => { const el = $(`[data-cmd="${CSS.escape(t.dataset.cmdRun)}"]`); if (done !== false && el) el.value = ""; }); // champ recréé par le rendu
  }
});
async function rotateRoot() {
  const word = await ask({ title: "Régénérer toute la chaîne", danger: true, ok: "Régénérer", input: true, pattern: "RACINE", hint: "taper RACINE en majuscules",
    text: "Nouvelle racine, toutes les CA, tous les certificats (nouvelles clés).\nLes VMs ne feront plus confiance aux certificats tant qu'elles n'auront pas la nouvelle racine. L'ancienne PKI est archivée.\n\nTaper RACINE pour confirmer :" });
  if (word === "RACINE") run(["pki", "renew", "root", "--yes", ...($("#rotate-trust")?.checked ? ["--trust-vms"] : [])]);
}

async function snapshotVM(m, vm) {
  const name = await ask({ title: `Snapshot de ${vm}`, text: "Un snapshot existant du même nom est remplacé.\nNom : lettres, chiffres, . _ -",
    input: true, value: "clean", pattern: "[A-Za-z0-9._\\-]+", hint: "lettres, chiffres, . _ - (sans espace)", ok: "Prendre le snapshot" });
  if (name) run([m, "snapshot", vm, name]);
}

// Choix du snapshot et confirmation dans la même boîte
async function restoreVM(m, vm, snaps) {
  const text = `L'état actuel de ${vm} sera perdu.`;
  const name = snaps.length === 1
    ? (await ask({ title: `Restaurer ${vm} au snapshot « ${snaps[0]} » ?`, text, ok: "Restaurer", danger: true }) && snaps[0])
    : await ask({ title: `Restaurer ${vm}`, text: text + "\nSnapshot :", options: snaps, value: snaps[0], ok: "Restaurer", danger: true });
  if (name && snaps.includes(name)) run([m, "restore", vm, name]);
}

document.addEventListener("keydown", e => {
  if (e.key === "Enter" && e.target.dataset?.cmd) $(`[data-cmd-run="${CSS.escape(e.target.dataset.cmd)}"]`).click();
});
$("#refresh").onclick = () => load(true);
$("#log-toggle").onclick = () => {
  const l = $("#log"); l.classList.toggle("collapsed");
  const shut = l.classList.contains("collapsed");
  $("#log-toggle").textContent = shut ? "Afficher" : "Masquer";
  if (!shut) $("#console-in").focus();
};
$("#log-clear").onclick = () => { $("#log-out").textContent = ""; };
