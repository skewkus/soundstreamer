"use strict";
(() => {
  const themes = ["green","blue","red","purple","amber","teal"];
  async function refresh() {
    try {
      const response = await fetch("/api/theme",{cache:"no-store",signal:AbortSignal.timeout(5000)});
      if (!response.ok) return;
      const {theme} = await response.json();
      if (themes.includes(theme)) document.documentElement.dataset.theme=theme;
    } catch {}
  }
  refresh();setInterval(refresh,1000);
})();
