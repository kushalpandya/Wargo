// Applies the saved theme before first paint (external file to satisfy CSP).
(function () {
  try {
    var m = localStorage.getItem("wargo.theme") || "auto";
    var dark = m === "dark" || (m === "auto" && matchMedia("(prefers-color-scheme: dark)").matches);
    document.documentElement.classList.toggle("dark", dark);
    document.documentElement.dataset.theme = m;
    var a = localStorage.getItem("wargo.accent");
    if (a) document.documentElement.dataset.accent = a;
  } catch (e) {}
})();
