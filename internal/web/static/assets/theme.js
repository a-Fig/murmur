// Runs before the page paints, so a dark page never flashes light. The choice ("system",
// "light" or "dark") is the browser's, kept in localStorage; the daemon never sees it.
(function () {
  var name = document.querySelector('meta[name="app-name"]').content;
  var key = name + ":appearance";
  var media = window.matchMedia("(prefers-color-scheme: dark)");
  function apply() {
    var choice = localStorage.getItem(key) || "system";
    var dark = choice === "dark" || (choice === "system" && media.matches);
    document.documentElement.classList.toggle("dark", dark);
    document.documentElement.classList.toggle("murmur", choice === "murmur");
  }
  apply();
  media.addEventListener("change", apply);
  window.appearance = {
    get: function () {
      return localStorage.getItem(key) || "system";
    },
    set: function (choice) {
      if (choice === "system") localStorage.removeItem(key);
      else localStorage.setItem(key, choice);
      apply();
    },
  };
})();
