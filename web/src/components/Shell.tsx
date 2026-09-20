import type { ReactNode } from "react";
import type { Session, Settings, Status } from "../api";
import { Sidebar } from "./Sidebar";
export function Shell({ path, session, status, settings, navigate, onLogout, children }: { path: string; session: Session; status: Status | null; settings: Settings | null; navigate: (to: string) => void; onLogout: () => void; children: ReactNode }) {
  return <div className="shell"><Sidebar path={path} session={session} status={status} settings={settings} navigate={navigate} onLogout={onLogout} /><main className="content"><div className="page-enter" key={path}>{children}</div></main></div>;
}
