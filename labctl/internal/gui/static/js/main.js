// GUI de labctl — point d'entrée (seul script chargé par index.html) : charge les modules dans l'ordre, puis
// démarre l'interface. Les modules qui ne font qu'installer des écouteurs sont importés pour cet effet.
import "./run.js";
import "./render.js";
import "./pending.js";
import "./net.js";
import "./files.js";
import "./gateway.js";
import "./pki.js";
import "./editor.js";
import "./actions.js";
import "./console.js";
import { $, core, go, load, loadConfig, loadCore, page, running, token } from "./core.js";
import { loadTraffic } from "./traffic.js";

if (!token) $("#view-overview").innerHTML = `<div class="empty err">Jeton absent : ouvre l'adresse complète affichée par « labctl gui ».</div>`;
else {
  // hauteur réelle de l'en-tête, pour le panneau latéral collant
  document.documentElement.style.setProperty("--hdr", $("header").offsetHeight + "px");
  let p = new URLSearchParams(location.hash.slice(1)).get("p");
  if (!p) try { p = localStorage.getItem("labs-page"); } catch {}
  go(p); load(true);
  setInterval(() => {
    if (running || document.hidden) return;
    load();
    if (page === "core") loadCore();
    if (page === "traffic") loadTraffic();
  }, 5000);
  loadConfig(); // changements en attente sur la passerelle
}
