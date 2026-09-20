import { useEffect, useState } from "react";

export type ThemePreference = "light" | "dark" | "system";

const query = "(prefers-color-scheme: dark)";

function readTheme(): ThemePreference {
  const saved = localStorage.getItem("pgfy-theme");
  return saved === "light" || saved === "system" ? saved : "dark";
}

function applyTheme(preference: ThemePreference) {
  const resolved = preference === "system" ? (matchMedia(query).matches ? "dark" : "light") : preference;
  document.documentElement.dataset.theme = resolved;
  document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')?.setAttribute("content", resolved === "dark" ? "#0e1311" : "#f5f7f5");
}

export function useTheme() {
  const [theme, setThemeState] = useState<ThemePreference>(readTheme);
  useEffect(() => {
    applyTheme(theme);
    localStorage.setItem("pgfy-theme", theme);
    if (theme !== "system") return;
    const media = matchMedia(query);
    const update = () => applyTheme("system");
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, [theme]);
  return { theme, setTheme: setThemeState };
}
