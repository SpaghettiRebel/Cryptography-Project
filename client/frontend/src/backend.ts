// Единственная точка связи с Go: обёртки привязок Wails, разбор ошибок и подписка на события.
// Интерфейс не получает ключей и не выполняет криптографических операций.
import * as Go from "../wailsjs/go/main/App";
import { EventsOn } from "../wailsjs/runtime/runtime";

export type Connection = "online" | "offline" | "connecting";
export type KeyState = "NONE" | "WAITING_SELF" | "WAITING_PEER" | "READY" | "KEY_LOST" | "PEER_VALUE_INVALID";

export type Settings = { serverAddress: string; certificatePath: string; recentLogins: string[] };

export type ChatView = {
  id: string;
  peer: string;
  creator: string;
  algorithm: string;
  keyBits: number;
  cipherMode: string;
  padding: string;
  createdAt: number;
  selfConnected: boolean;
  peerConnected: boolean;
  keyState: KeyState;
  algorithmNote: string;
};

export type Snapshot = {
  version: number;
  connection: Connection;
  session: { login: string; userId: string } | null;
  contacts: string[];
  incoming: string[];
  outgoing: string[];
  chats: ChatView[];
};

export type AlgorithmOption = { id: string; available: boolean; substitute: boolean; note: string };
export type Option = { id: string; title: string; warning: string };
export type ChatOptions = { algorithms: AlgorithmOption[]; keyBits: number[]; cipherModes: Option[]; paddings: Option[] };
export type CreateChatRequest = { peer: string; algorithm: string; keyBits: number; cipherMode: string; padding: string };

export type Notice = { kind: "info" | "warning" | "error"; code: string; message: string };
export type SessionEnded = { code: "SESSION_EXPIRED" | "LOGGED_OUT"; login: string };
export type AppError = { code: string; message: string; field?: string };

const internal: AppError = { code: "INTERNAL", message: "Внутренняя ошибка клиента" };

/** Разбирает ошибку Go (строка JSON) в {code, message, field}; всё остальное — INTERNAL. */
export function parseError(e: unknown): AppError {
  const text = typeof e === "string" ? e : e instanceof Error ? e.message : null;
  if (text === null) return internal;
  try {
    const v = JSON.parse(text) as Partial<AppError>;
    if (v && typeof v.code === "string" && typeof v.message === "string") {
      return typeof v.field === "string" && v.field !== ""
        ? { code: v.code, message: v.message, field: v.field }
        : { code: v.code, message: v.message };
    }
  } catch {
    // не JSON
  }
  return internal;
}

const snapshot = (p: Promise<unknown>) => p as Promise<Snapshot>;

export const api = {
  frontendReady: () => Go.FrontendReady(),
  getSettings: () => Go.GetSettings() as Promise<Settings>,
  saveSettings: (s: Settings) => Go.SaveSettings(s as never) as Promise<Settings>,
  chooseCertificate: () => Go.ChooseCertificate(),
  checkServer: () => Go.CheckServer(),
  register: (login: string, password: string) => Go.Register(login, password),
  login: (login: string, password: string) => snapshot(Go.Login(login, password)),
  logout: () => Go.Logout(),
  getSnapshot: () => snapshot(Go.GetSnapshot()),
  sendContactRequest: (login: string) => Go.SendContactRequest(login) as Promise<{ becameContact: boolean }>,
  acceptContact: (login: string) => Go.AcceptContact(login),
  rejectContact: (login: string) => Go.RejectContact(login),
  removeContact: (login: string) => Go.RemoveContact(login),
  chatOptions: () => Go.ChatOptions() as Promise<ChatOptions>,
  createChat: (req: CreateChatRequest) => Go.CreateChat(req as never) as Promise<ChatView>,
  connectChat: (id: string) => Go.ConnectChat(id) as Promise<ChatView>,
  disconnectChat: (id: string) => Go.DisconnectChat(id) as Promise<ChatView>,
  closeChat: (id: string) => Go.CloseChat(id),
};

export type Handlers = {
  snapshot: (s: Snapshot) => void;
  notice: (n: Notice) => void;
  sessionEnded: (e: SessionEnded) => void;
};

/** Подписывается на события Go; возвращает отписку. */
export function subscribe(h: Handlers): () => void {
  const offs = [
    EventsOn("snapshot", (s: Snapshot) => h.snapshot(s)),
    EventsOn("notice", (n: Notice) => h.notice(n)),
    EventsOn("session-ended", (e: SessionEnded) => h.sessionEnded(e)),
  ];
  return () => offs.forEach((off) => off());
}
