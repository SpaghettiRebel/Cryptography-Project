// Подмена привязок Wails для Vitest: методы Go — vi.fn, события — локальная шина.
import { vi } from "vitest";

const listeners = new Map<string, Set<(...data: unknown[]) => void>>();

export const EventsOn = vi.fn((name: string, cb: (...data: unknown[]) => void) => {
  if (!listeners.has(name)) listeners.set(name, new Set());
  listeners.get(name)!.add(cb);
  return () => listeners.get(name)?.delete(cb);
});
export const EventsOff = vi.fn((name: string) => listeners.delete(name));

/** Отправляет событие, как будто его прислал Go. */
export function emit(name: string, data: unknown) {
  for (const cb of [...(listeners.get(name) ?? [])]) cb(data);
}

export const emptySnapshot = {
  version: 0,
  connection: "connecting",
  session: null,
  contacts: [],
  incoming: [],
  outgoing: [],
  chats: [],
};

export const defaultOptions = {
  algorithms: [
    { id: "MAGENTA", available: false, substitute: false, note: "алгоритм не реализован в этой сборке" },
    { id: "RC6", available: false, substitute: false, note: "алгоритм не реализован в этой сборке" },
  ],
  keyBits: [128, 192, 256],
  cipherModes: [
    { id: "ECB", title: "ECB", warning: "ECB раскрывает повторы" },
    { id: "CBC", title: "CBC", warning: "" },
    { id: "PCBC", title: "PCBC", warning: "" },
    { id: "CFB", title: "CFB", warning: "" },
    { id: "OFB", title: "OFB", warning: "" },
    { id: "CTR", title: "CTR", warning: "" },
    { id: "RANDOM_DELTA", title: "Random Delta", warning: "" },
  ],
  paddings: [
    { id: "ZEROS", title: "Zeros", warning: "Zeros не отличает нули от набивки" },
    { id: "ANSI_X923", title: "ANSI X.923", warning: "" },
    { id: "PKCS7", title: "PKCS7", warning: "" },
    { id: "ISO_10126", title: "ISO 10126", warning: "" },
  ],
};

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type GoFn = (...args: any[]) => Promise<any>;

export const FrontendReady = vi.fn<GoFn>(async () => undefined);
export const GetSettings = vi.fn<GoFn>(async () => ({
  serverAddress: "localhost:8443",
  certificatePath: "C:/certs/server.crt",
  recentLogins: [] as string[],
}));
export const SaveSettings = vi.fn<GoFn>(async (s: unknown) => s);
export const ChooseCertificate = vi.fn<GoFn>(async () => "");
export const CheckServer = vi.fn<GoFn>(async () => undefined);
export const Register = vi.fn<GoFn>(async () => undefined);
export const Login = vi.fn<GoFn>(async () => ({ ...emptySnapshot }));
export const Logout = vi.fn<GoFn>(async () => undefined);
export const GetSnapshot = vi.fn<GoFn>(async () => ({ ...emptySnapshot }));
export const SendContactRequest = vi.fn<GoFn>(async () => ({ becameContact: false }));
export const AcceptContact = vi.fn<GoFn>(async () => undefined);
export const RejectContact = vi.fn<GoFn>(async () => undefined);
export const RemoveContact = vi.fn<GoFn>(async () => undefined);
export const ChatOptions = vi.fn<GoFn>(async () => defaultOptions);
export const CreateChat = vi.fn<GoFn>(async () => ({}));
export const ConnectChat = vi.fn<GoFn>(async () => ({}));
export const DisconnectChat = vi.fn<GoFn>(async () => ({}));
export const CloseChat = vi.fn<GoFn>(async () => undefined);
export const ListMessages = vi.fn<GoFn>(async () => ({ messages: [], hasMore: false }));
export const SendText = vi.fn<GoFn>(async () => ({}));
export const SendFile = vi.fn<GoFn>(async () => null);
export const CancelMessage = vi.fn<GoFn>(async () => ({}));
export const RetryDecrypt = vi.fn<GoFn>(async () => ({}));
export const SaveFile = vi.fn<GoFn>(async () => "");
export const MarkRead = vi.fn<GoFn>(async () => undefined);
export const ReceiveHere = vi.fn<GoFn>(async () => ({}));
export const ListClosedChats = vi.fn<GoFn>(async () => []);
export const DeleteChatHistory = vi.fn<GoFn>(async () => undefined);

const all = {
  FrontendReady, GetSettings, SaveSettings, ChooseCertificate, CheckServer, Register, Login, Logout, GetSnapshot,
  SendContactRequest, AcceptContact, RejectContact, RemoveContact, ChatOptions, CreateChat, ConnectChat,
  DisconnectChat, CloseChat, ListMessages, SendText, SendFile, CancelMessage, RetryDecrypt, SaveFile, MarkRead,
  ReceiveHere, ListClosedChats, DeleteChatHistory,
};
const defaults = new Map(Object.entries(all).map(([k, f]) => [k, f.getMockImplementation()]));

/** Возвращает подмену к начальному состоянию. */
export function resetWails() {
  listeners.clear();
  for (const [k, f] of Object.entries(all)) {
    f.mockReset();
    f.mockImplementation(defaults.get(k) as never);
  }
}

/** Ошибка Go в том виде, в каком её передаёт Wails: строка JSON. */
export function goError(code: string, message = "сообщение " + code, field?: string) {
  return JSON.stringify(field ? { code, message, field } : { code, message });
}
