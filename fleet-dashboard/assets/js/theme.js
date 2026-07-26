/* Fleet Dashboard — theme + layout init (load in <head>, before paint).
   Applies the saved theme and collapsed-panel state so there's no flash. */
(function () {
  try {
    var r = document.documentElement;
    var t = localStorage.getItem("fleet-theme") ||
      (matchMedia("(prefers-color-scheme:dark)").matches ? "dark" : "light");
    r.dataset.theme = t;
    if (localStorage.getItem("fleet-side") === "off") r.dataset.side = "off";
    if (localStorage.getItem("fleet-rail") === "off") r.dataset.rail = "off";
  } catch (e) {}
})();
