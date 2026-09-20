import { Check, TriangleAlert } from "lucide-react";
import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";

type ToastContextValue = { showToast: (message: string, tone?: "good" | "bad") => void };
const ToastContext = createContext<ToastContextValue | null>(null);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<{ id: number; message: string; tone: "good" | "bad" }[]>([]);
  const showToast = useCallback((message: string, tone: "good" | "bad" = "good") => {
    const id = Date.now();
    setToasts((items) => [...items, { id, message, tone }]);
    setTimeout(() => setToasts((items) => items.filter((item) => item.id !== id)), 3000);
  }, []);
  const value = useMemo(() => ({ showToast }), [showToast]);
  return <ToastContext.Provider value={value}>{children}<div className="toast-region" aria-live="polite">{toasts.map((toast) => <div className={`toast toast-${toast.tone}`} key={toast.id}><span className="toast-icon">{toast.tone === "good" ? <Check size={15} /> : <TriangleAlert size={15} />}</span>{toast.message}</div>)}</div></ToastContext.Provider>;
}
export function useToast() {
  const value = useContext(ToastContext);
  if (!value) throw new Error("useToast must be used inside ToastProvider");
  return value;
}
