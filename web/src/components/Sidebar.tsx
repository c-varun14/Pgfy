import { Database, HardDrive, LogOut, Monitor, Moon, Settings2, Sun } from "lucide-react";
import type { Session, Settings, Status } from "../api";
import { type ThemePreference, useTheme } from "../theme";
import { SegmentedControl } from "./ui/segmented-control";
import { Tooltip } from "./ui/tooltip";

const nav = [
  { label: "Databases", path: "/", icon: Database },
  { label: "Backups", path: "/backups", icon: HardDrive },
  { label: "Settings", path: "/settings", icon: Settings2 },
];

export function BrandMark({ size = 26 }: { size?: number }) {
  return (
    <span className="brand-mark" aria-hidden="true">
      <Database size={Math.round(size * 0.58)} strokeWidth={2.4} />
    </span>
  );
}

export function Brand({ onClick }: { onClick?: () => void }) {
  return (
    <button type="button" className="brand" aria-label="Pgfy home" onClick={onClick}>
      <BrandMark />
      <span className="brand-text">pgfy<span className="brand-dot">.</span></span>
    </button>
  );
}

export function Sidebar({ path, session, status, settings, navigate, onLogout }: { path: string; session: Session; status: Status | null; settings: Settings | null; navigate: (to: string) => void; onLogout: () => void }) {
  const { theme, setTheme } = useTheme();
  const active = path.startsWith("/projects/") ? "/" : path === "/recovery" ? "/backups" : path;
  const themes: { value: ThemePreference; label: string; icon: React.ReactNode }[] = [
    { value: "light", label: "Light", icon: <Sun size={14} /> },
    { value: "system", label: "System", icon: <Monitor size={14} /> },
    { value: "dark", label: "Dark", icon: <Moon size={14} /> },
  ];
  const healthy = status?.ready !== false;
  return (
    <aside className="sidebar">
      <div className="sidebar-main">
        <Brand onClick={() => navigate("/")} />
        <nav aria-label="Main navigation">
          {nav.map((item) => {
            const Icon = item.icon;
            return (
              <button type="button" key={item.path} className={active === item.path ? "active" : ""} onClick={() => navigate(item.path)}>
                <Icon size={17} />
                <span>{item.label}</span>
              </button>
            );
          })}
        </nav>
      </div>
      <div className="sidebar-bottom">
        <div className="server-health">
          <span className={healthy ? "health-dot" : "health-dot bad"} />
          <strong>{healthy ? "Healthy" : "Needs attention"}</strong>
          <code title={settings?.hostname || "SSH tunnel"}>{settings?.hostname || "SSH tunnel"}</code>
        </div>
        <div className="sidebar-row">
          <SegmentedControl value={theme} options={themes} onChange={setTheme} label="Appearance" />
          <div className="account">
            <Tooltip text={session.email}><span className="avatar" aria-label={session.email}>{session.email.slice(0, 1)}</span></Tooltip>
            <Tooltip text="Sign out"><button type="button" className="icon-button" aria-label="Sign out" onClick={onLogout}><LogOut size={15} /></button></Tooltip>
          </div>
        </div>
      </div>
    </aside>
  );
}
