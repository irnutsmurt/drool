// Apply the saved appearance before the first paint (no flash of the wrong
// theme). A separate file: the CSP allows only same-origin scripts.
(function () {
  var t = "system";
  try {
    t = localStorage.getItem("drool.theme") || "system";
  } catch (e) {}
  if (t !== "light" && t !== "dark") t = "system";
  document.documentElement.dataset.theme = t;
})();
