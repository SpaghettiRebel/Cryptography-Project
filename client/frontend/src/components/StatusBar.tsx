import type { Connection } from "../backend";

export const connectionText: Record<Connection, string> = {
  online: "в сети",
  offline: "нет связи",
  connecting: "подключение…",
};

type Props = {
  login: string;
  connection: Connection;
  loggingOut: boolean;
  onLogout: () => void;
};

/** Верхняя строка главного окна: пользователь, состояние связи, выход. */
export default function StatusBar({ login, connection, loggingOut, onLogout }: Props) {
  return (
    <header className="statusbar">
      <span className="who" data-testid="current-login">
        {login}
      </span>
      <span className={`connection connection-${connection}`} data-testid="connection">
        {connectionText[connection]}
      </span>
      <button type="button" onClick={onLogout} disabled={loggingOut} data-testid="logout">
        {loggingOut ? "Выход…" : "Выйти"}
      </button>
    </header>
  );
}
