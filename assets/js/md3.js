/* ============================================================================
   Fleet — Material Design 3 interactions
   ----------------------------------------------------------------------------
   • Ripple: the MD3 touch ripple on buttons, nav items, chips, list rows.
   • App-bar elevation: the top bar gains elevation once content scrolls under.
   Zero dependencies. Respects prefers-reduced-motion.
   ============================================================================ */
(function () {
  var RIPPLE_SEL = ".btn,.iconbtn,.fab,.nav a,.segbtn,.kebab,.toggle button," +
    ".seggroup button,.menu-item,.pal-list li,.chip-btn,.segbtn";
  var reduce = matchMedia("(prefers-reduced-motion:reduce)").matches;

  // ---- ripple -------------------------------------------------------------
  document.addEventListener("pointerdown", function (e) {
    if (reduce || e.button !== 0) return;
    var host = e.target.closest(RIPPLE_SEL);
    if (!host || host.hasAttribute("disabled")) return;

    var rect = host.getBoundingClientRect();
    var size = Math.max(rect.width, rect.height) * 2;
    var ink = document.createElement("span");
    ink.className = "ripple-ink";
    ink.style.width = ink.style.height = size + "px";
    ink.style.left = (e.clientX - rect.left - size / 2) + "px";
    ink.style.top = (e.clientY - rect.top - size / 2) + "px";
    host.appendChild(ink);
    ink.addEventListener("animationend", function () { ink.remove(); });
  }, { passive: true });

  // ---- app-bar elevation on scroll ---------------------------------------
  function wireAppBar() {
    var bar = document.querySelector(".topbar");
    var scroller = document.querySelector("main");
    if (!bar || !scroller) return;
    var onScroll = function () { bar.classList.toggle("scrolled", scroller.scrollTop > 2); };
    scroller.addEventListener("scroll", onScroll, { passive: true });
    onScroll();
  }
  if (document.readyState !== "loading") wireAppBar();
  else document.addEventListener("DOMContentLoaded", wireAppBar);
})();
