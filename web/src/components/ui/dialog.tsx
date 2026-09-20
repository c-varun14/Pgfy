import { useEffect, useRef, type ReactNode } from "react";
import { X } from "lucide-react";

export function Dialog({ open, onClose, title, children, className = "" }: { open: boolean; onClose: () => void; title: string; children: ReactNode; className?: string }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    if (open && !dialog.open) dialog.showModal();
    if (!open && dialog.open) dialog.close();
  }, [open]);
  return <dialog ref={ref} className={`dialog ${className}`} onCancel={onClose} onClose={onClose} onClick={(event) => { if (event.target === ref.current) onClose(); }} aria-labelledby="dialog-title"><div className="dialog-panel"><header><h2 id="dialog-title">{title}</h2><button type="button" className="icon-button" aria-label="Close" onClick={onClose}><X size={18} /></button></header>{children}</div></dialog>;
}
