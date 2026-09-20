import { useCallback, useEffect, useState } from "react";

/** Path-based routing without a router dependency; the server serves index.html for every path. */
export function useRoute() {
  const [path, setPath] = useState(location.pathname);
  const [search, setSearch] = useState(location.search);
  useEffect(() => {
    const onPop = () => { setPath(location.pathname); setSearch(location.search); };
    addEventListener("popstate", onPop);
    return () => removeEventListener("popstate", onPop);
  }, []);
  const navigate = useCallback((to: string) => {
    if (to !== `${location.pathname}${location.search}`) history.pushState(null, "", to);
    setPath(location.pathname);
    setSearch(location.search);
    scrollTo(0, 0);
  }, []);
  const params = new URLSearchParams(search);
  const redirect = useCallback((to: string) => {
    history.replaceState(null, "", to);
    setPath(location.pathname);
    setSearch(location.search);
  }, []);
  return { path, search, params, navigate, redirect };
}
