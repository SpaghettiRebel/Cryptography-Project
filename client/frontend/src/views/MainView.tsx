import { useState } from "react";
import { api } from "../backend";
import Banner from "../components/Banner";
import StatusBar from "../components/StatusBar";
import { emptySnapshot, getState, setState, useStore } from "../store";
import { useAction } from "../useAction";
import ChatsTab from "./ChatsTab";
import ContactsTab from "./ContactsTab";

type Tab = "chats" | "contacts";

/** Главное окно: строка состояния и вкладки «Чаты» и «Контакты». */
export default function MainView() {
  const snapshot = useStore((s) => s.snapshot);
  const [tab, setTab] = useState<Tab>("chats");
  const logout = useAction(api.logout);

  const onLogout = async () => {
    const login = snapshot.session?.login ?? "";
    await logout.run();
    // Выход завершается локально всегда.
    setState({
      screen: "login",
      snapshot: { ...emptySnapshot, connection: getState().snapshot.connection },
      loginPrefill: login,
      banner: null,
    });
  };

  return (
    <div className="screen main">
      <StatusBar
        login={snapshot.session?.login ?? ""}
        connection={snapshot.connection}
        loggingOut={logout.busy}
        onLogout={onLogout}
      />
      {snapshot.connection === "offline" && (
        <Banner
          kind="warning"
          testId="offline-banner"
          text="Нет связи с сервером: действия, требующие сервера, недоступны, показано последнее известное состояние"
        />
      )}
      <div role="tablist" className="tabs">
        <button type="button" role="tab" aria-selected={tab === "chats"} onClick={() => setTab("chats")}>
          Чаты{snapshot.chats.length > 0 ? ` (${snapshot.chats.length})` : ""}
        </button>
        <button type="button" role="tab" aria-selected={tab === "contacts"} onClick={() => setTab("contacts")}>
          Контакты{snapshot.incoming.length > 0 ? ` · запросов: ${snapshot.incoming.length}` : ""}
        </button>
      </div>
      {tab === "chats" ? <ChatsTab onOpenContacts={() => setTab("contacts")} /> : <ContactsTab />}
    </div>
  );
}
