// Applies the saved theme before first paint (external file to satisfy CSP).
(function () {
  try {
    var m = localStorage.getItem("wargo.theme") || "auto";
    var dark = m === "dark" || (m === "auto" && matchMedia("(prefers-color-scheme: dark)").matches);
    document.documentElement.classList.toggle("dark", dark);
    document.documentElement.dataset.theme = m;
    // Status bar colour = the page background (--bg); keep in sync with THEME_BG in src/lib/theme.ts.
    var tc = document.getElementById("theme-color");
    if (tc) tc.setAttribute("content", dark ? "#06070d" : "#f5f5fa");
    // Installed to the Home Screen: a flat top so the status bar blends in (styles/index.css).
    if (matchMedia("(display-mode: standalone)").matches || navigator.standalone === true) {
      document.documentElement.dataset.standalone = "1";
    }
    var a = localStorage.getItem("wargo.accent");
    if (a) document.documentElement.dataset.accent = a;
  } catch (e) {}
})();
