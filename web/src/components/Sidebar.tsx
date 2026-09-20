import { Database, HardDrive, LogOut, Monitor, Moon, Settings2, Sun } from "lucide-react";
import type { Session, Settings, Status } from "../api";
import { type ThemePreference, useTheme } from "../theme";
import { SegmentedControl } from "./ui/segmented-control";

const nav = [
  { label: "Databases", path: "/", icon: Database },
  { label: "Backups", path: "/backups", icon: HardDrive },
  { label: "Settings", path: "/settings", icon: Settings2 },
];

export function Brand() { return <button type="button" className="brand" aria-label="Pgfy home">pgfy<span>.</span></button>; }

export function Sidebar({ path, session, status, settings, navigate, onLogout }: { path: string; session: Session; status: Status | null; settings: Settings | null; navigate: (to: string) => void; onLogout: () => void }) {
  const { theme, setTheme } = useTheme();
  const active = path.startsWith("/projects/") ? "/" : path === "/recovery" ? "/backups" : path;
  const themes: { value: ThemePreference; label: string; icon: React.ReactNode }[] = [
    { value: "light", label: "Light", icon: <Sun size={15} /> },
    { value: "system", label: "System", icon: <Monitor size={15} /> },
    { value: "dark", label: "Dark", icon: <Moon size={15} /> },
  ];
  return <aside className="sidebar"><div className="sidebar-main"><Brand /><nav aria-label="Main navigation">{nav.map((item) => { const Icon = item.icon; return <button type="button" key={item.path} className={active === item.path ? "active" : ""} onClick={() => navigate(item.path)}><Icon size={18} />{item.label}</button>; })}</nav></div><div className="sidebar-bottom"><div className="server-health"><span className={status?.ready === false ? "health-dot bad" : "health-dot"} /><span><strong>{status?.ready === false ? "Needs attention" : "Server healthy"}</strong><small>{settings?.hostname || "SSH tunnel"}</small></span></div><SegmentedControl value={theme} options={themes} onChange={setTheme} label="Appearance" /><div className="account"><span title={session.email}>{session.email}</span><button type="button" onClick={onLogout}><LogOut size={15} />Sign out</button></div></div></aside>;
}
