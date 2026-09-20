import { useEffect, useState } from "react";
import { Check, Copy } from "lucide-react";
import { Button } from "./button";
import { useToast } from "./toast";

export function CopyButton({ value, label = "Copy", toast = "Copied" }: { value: string; label?: string; toast?: string }) {
  const { showToast } = useToast();
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(timer);
  }, [copied]);
  return (
    <Button variant="ghost" size="sm" type="button" data-copied={copied || undefined} onClick={() => void navigator.clipboard.writeText(value).then(() => { setCopied(true); showToast(toast); })}>
      {copied ? <Check size={14} className="pop" /> : <Copy size={14} />}
      {copied ? "Copied" : label}
    </Button>
  );
}
