import { useCallback, useEffect, useState } from "react";

/** Path-based routing without a router dependency; the server serves index.html for every path. */
export function useRoute() {
  const [path, setPath] = useState(location.pathname);
  useEffect(() => {
    const onPop = () => setPath(location.pathname);
    addEventListener("popstate", onPop);
    return () => removeEventListener("popstate", onPop);
  }, []);
  const navigate = useCallback((to: string) => {
    if (to !== location.pathname) history.pushState(null, "", to);
    setPath(to);
    scrollTo(0, 0);
  }, []);
  return { path, navigate };
}
