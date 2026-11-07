// GUI de labctl — démarrage. Scripts classiques chargés dans l'ordre par index.html (portée globale
// partagée) : une fonction n'est utilisable qu'après le chargement du fichier qui la définit.
"use strict";

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
