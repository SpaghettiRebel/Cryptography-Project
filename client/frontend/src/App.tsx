import { useEffect } from "react";
import { api, subscribe } from "./backend";
import Notices from "./components/Notices";
import { applySnapshot, emptySnapshot, getState, pushNotice, setState, useStore } from "./store";
import LoginView from "./views/LoginView";
import MainView from "./views/MainView";
import SettingsView from "./views/SettingsView";

export default function App() {
  const screen = useStore((s) => s.screen);

  useEffect(() => {
    const off = subscribe({
      snapshot: (s) => {
        applySnapshot(s);
        if (s.session && getState().screen === "login") setState({ screen: "main" });
      },
      notice: (n) => {
        pushNotice(n);
      },
      sessionEnded: (e) => {
        setState({
          screen: "login",
          snapshot: { ...emptySnapshot, connection: getState().snapshot.connection },
          banner: e.code === "SESSION_EXPIRED" ? "Сессия истекла, войдите снова" : null,
          loginPrefill: e.login,
        });
      },
    });
    api.getSnapshot().then(
      (s) => {
        applySnapshot(s);
        if (s.session) setState({ screen: "main" });
      },
      () => undefined,
    );
    // Go узнаёт, что интерфейс загружен (пробный запуск окна).
    api.frontendReady().catch(() => undefined);
    return off;
  }, []);

  return (
    <div className="app">
      {screen === "login" && <LoginView />}
      {screen === "settings" && <SettingsView />}
      {screen === "main" && <MainView />}
      <Notices />
    </div>
  );
}
