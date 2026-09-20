(function () {
  var preference = localStorage.getItem("pgfy-theme") || "dark";
  var dark = preference === "dark" || (preference === "system" && matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.dataset.theme = dark ? "dark" : "light";
})();
