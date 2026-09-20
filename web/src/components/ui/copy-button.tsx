import { Check, Copy } from "lucide-react";
import { Button } from "./button";
import { useToast } from "./toast";
export function CopyButton({ value, label = "Copy", toast = "Copied" }: { value: string; label?: string; toast?: string }) {
  const { showToast } = useToast();
  return <Button variant="ghost" size="sm" type="button" onClick={() => void navigator.clipboard.writeText(value).then(() => showToast(toast))}><Copy size={14} />{label}</Button>;
}
