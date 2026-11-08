// GUI de labctl — exécution d'une commande labctl, sortie au fil de l'eau.
// Module ES : ce qu'il partage est exporté, ce qu'il utilise est importé.
import { $, api, ask, core, current, esc, load, loadConfig, loadCore, loadPKI, page, pki, running, setCurrent, setRunning, toast } from "./core.js";
import { render } from "./render.js";
import { paintEditor } from "./editor.js";

// --- Exécution d'une commande labctl, sortie affichée au fil de l'eau
// confirmText : question posée avant (danger : bouton rouge). Le journal n'est pas ouvert d'office : l'action
// en cours s'affiche à sa place (ligne de la VM, module), et une notification donne le résultat.
export async function run(args, confirmText, danger = false) {
  if (running) return false;
  if (confirmText && !(await ask({ title: "Confirmer", text: confirmText, ok: danger ? "Oui, continuer" : "Continuer", danger }))) return false;
  setRunning(true); setCurrent(args); render(); paintEditor();
  const log = $("#log-out");
  const shown = `labctl ${args.map(a => /[\s'"]/.test(a) || !a ? `'${a.replace(/'/g, "'\\''")}'` : a).join(" ")}`;
  $("#log-status").innerHTML = `<span class="busy"><span class="spin"></span>${esc(shown)}</span>`;
  log.textContent += `\n$ ${shown}\n`;
  let out = "", ok = false;
  try {
    const r = await api("/api/run", { method: "POST", body: JSON.stringify({ args }) });
    const reader = r.body.getReader(), dec = new TextDecoder();
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      // Retire les codes couleur ANSI
      const chunk = dec.decode(value, { stream: true }).replace(/\x1b\[[0-9;]*m/g, "");
      out += chunk; log.textContent += chunk;
      log.scrollTop = log.scrollHeight;
    }
    ok = out.trimEnd().endsWith("[terminé]");
    $("#log-status").textContent = "";
  } catch (e) {
    out += e.message;
    log.textContent += `[erreur : ${e.message}]\n`;
    $("#log-status").innerHTML = `<span class="err">${esc(e.message)}</span>`;
  } finally {
    setRunning(false); setCurrent(null); log.scrollTop = log.scrollHeight;
    if (ok) toast(`${shown} : terminé`, false, true);
    else {
      // dernière ligne utile de la sortie, pour savoir tout de suite pourquoi
      const why = out.split("\n").map(l => l.trim()).filter(l => l && !l.startsWith("[échec")).pop() || "";
      toast(`${shown} : échec${why ? " — " + why : ""}`, true, true);
    }
    paintEditor();
    await load(true);
    if (page === "core") await loadCore(true);
    if (page === "pki") await loadPKI();
    await loadConfig(); // changements en attente sur la passerelle
  }
  return ok;
}

export async function console_(vm) {
  try { await api("/api/console", { method: "POST", body: JSON.stringify({ vm }) }); }
  catch (e) { toast(e.message, true); }
}

// L'action en cours concerne-t-elle ce module (et cette VM) ? Réponse : son verbe, sinon ""
export const verbLabel = { up: "construction", start: "démarrage", stop: "arrêt", snapshot: "snapshot", restore: "restauration", certs: "certificats",
  destroy: "suppression", keyboard: "clavier", on: "ouverture d'Internet", off: "fermeture d'Internet" };
export function activeOn(m, vm, wideOnly = false) {
  const a = current;
  if (!a) return "";
  let mod, vms, verb;
  if (a[0] === "core" && a[1] === "internet") [verb, mod, vms] = [a[2], a[3], a.slice(4)];
  else [mod, verb, vms] = [a[0], a[1], a.slice(2)];
  if (mod !== m || (vm && vms.length && !vms.includes(vm)) || (wideOnly && vms.length)) return "";
  return verbLabel[verb] || verb || "commande";
}
export const doing = v => `<span class="doing"><span class="spin"></span>${esc(v)} en cours…</span>`;
