/* Fleet Dashboard — shared chrome interactions for pages that don't run the
   full dashboard engine (settings, components). Theme toggle, panel collapse,
   mobile nav. All handlers are null-guarded so any page can include it. */
(function () {
  var $ = function (s) { return document.querySelector(s); };
  var tb = $("#themebtn");
  if (tb) tb.addEventListener("click", function () {
    var d = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
    document.documentElement.dataset.theme = d;
    try { localStorage.setItem("fleet-theme", d); } catch (e) {}
  });
  function collapser(id, key, store) {
    var b = $("#" + id), r = document.documentElement; if (!b) return;
    var sync = function () { b.classList.toggle("on", r.dataset[key] === "off"); }; sync();
    b.addEventListener("click", function () {
      var off = r.dataset[key] === "off";
      if (off) delete r.dataset[key]; else r.dataset[key] = "off";
      try { localStorage.setItem(store, off ? "on" : "off"); } catch (e) {} sync();
    });
  }
  collapser("sidetgl", "side", "fleet-side");
  collapser("railtgl", "rail", "fleet-rail");
  var bg = $("#burger");
  if (bg) bg.addEventListener("click", function () { document.body.classList.toggle("nav-open"); });
})();
