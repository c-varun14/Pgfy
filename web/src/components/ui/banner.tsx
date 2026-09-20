import type { ReactNode } from "react";
export function Banner({ tone = "info", children, role }: { tone?: "info" | "warn" | "bad"; children: ReactNode; role?: "alert" }) {
  return <div className={`banner banner-${tone}`} role={role}>{children}</div>;
}
export function ErrorNotice({ message }: { message: string }) { return <Banner tone="bad" role="alert">{message}</Banner>; }
