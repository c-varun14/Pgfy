import { useCallback, useEffect, useState } from "react";

export type ThemePreference = "light" | "dark" | "system";

const query = "(prefers-color-scheme: dark)";
const changeEvent = "pgfy-theme-change";

function readTheme(): ThemePreference {
  const saved = localStorage.getItem("pgfy-theme");
  return saved === "light" || saved === "system" ? saved : "dark";
}

function applyTheme(preference: ThemePreference) {
  const resolved = preference === "system" ? (matchMedia(query).matches ? "dark" : "light") : preference;
  document.documentElement.dataset.theme = resolved;
  document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')?.setAttribute("content", resolved === "dark" ? "#111412" : "#f7f8f5");
}

export function useTheme() {
  const [theme, setThemeState] = useState<ThemePreference>(readTheme);
  useEffect(() => {
    const sync = (event: Event) => setThemeState((event as CustomEvent<ThemePreference>).detail);
    addEventListener(changeEvent, sync);
    return () => removeEventListener(changeEvent, sync);
  }, []);
  useEffect(() => {
    applyTheme(theme);
    localStorage.setItem("pgfy-theme", theme);
    if (theme !== "system") return;
    const media = matchMedia(query);
    const update = () => applyTheme("system");
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, [theme]);
  const setTheme = useCallback((preference: ThemePreference) => {
    setThemeState(preference);
    dispatchEvent(new CustomEvent(changeEvent, { detail: preference }));
  }, []);
  return { theme, setTheme };
}
