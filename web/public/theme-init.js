// Applies the saved theme before first paint (external file to satisfy CSP).
(function () {
  try {
    var m = localStorage.getItem("wargo.theme") || "auto";
    var dark = m === "dark" || (m === "auto" && matchMedia("(prefers-color-scheme: dark)").matches);
    document.documentElement.classList.toggle("dark", dark);
    document.documentElement.dataset.theme = m;
    var a = localStorage.getItem("wargo.accent");
    if (a) document.documentElement.dataset.accent = a;
    // Status bar colour = the page background (--bg), which takes the accent's
    // hue; keep in sync with THEME_BG in src/lib/theme.ts.
    var bg = {
      periwinkle: ["#f3f4fd", "#060610"],
      lilac: ["#f7f3fc", "#09050e"],
      sky: ["#eef7fc", "#01080f"],
      mint: ["#eef8f3", "#010a06"],
      rose: ["#fdf2f3", "#0f0406"],
      peach: ["#fcf3ee", "#0e0502"],
      mono: ["#f5f5f5", "#070707"],
    };
    var pair = bg[a] || bg.periwinkle;
    var tc = document.getElementById("theme-color");
    if (tc) tc.setAttribute("content", dark ? pair[1] : pair[0]);
    // Installed to the Home Screen: a flat top so the status bar blends in (styles/index.css).
    if (matchMedia("(display-mode: standalone)").matches || navigator.standalone === true) {
      document.documentElement.dataset.standalone = "1";
    }
  } catch (e) {}
})();
