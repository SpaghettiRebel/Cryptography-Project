// Состояние интерфейса: снимок от Go, уведомления и текущий экран.
import { useSyncExternalStore } from "react";
import type { Notice, Snapshot } from "./backend";

export type Screen = "login" | "settings" | "main";
export type ShownNotice = Notice & { id: number };

export type State = {
  screen: Screen;
  snapshot: Snapshot;
  notices: ShownNotice[];
  /** Сообщение над формой входа, например об истёкшей сессии. */
  banner: string | null;
  /** Логин, который подставляется в форму входа. */
  loginPrefill: string;
  /** Ошибка, из-за которой открыты настройки (CERT_INVALID, CERT_FILE). */
  settingsReason: string | null;
};

export const emptySnapshot: Snapshot = {
  version: 0,
  connection: "connecting",
  session: null,
  contacts: [],
  incoming: [],
  outgoing: [],
  chats: [],
};

const initial: State = {
  screen: "login",
  snapshot: emptySnapshot,
  notices: [],
  banner: null,
  loginPrefill: "",
  settingsReason: null,
};

let state: State = initial;
const listeners = new Set<() => void>();
let nextId = 1;

export function getState(): State {
  return state;
}

export function setState(patch: Partial<State>) {
  state = { ...state, ...patch };
  listeners.forEach((l) => l());
}

export function resetStore() {
  state = initial;
  nextId = 1;
  listeners.forEach((l) => l());
}

/** Принимает снимок, если он не старее показанного. */
export function applySnapshot(s: Snapshot) {
  if (s.version < state.snapshot.version && s.session && state.snapshot.session) return;
  setState({ snapshot: s });
}

export function pushNotice(n: Notice): number {
  const id = nextId++;
  setState({ notices: [...state.notices, { ...n, id }] });
  return id;
}

export function dismissNotice(id: number) {
  setState({ notices: state.notices.filter((n) => n.id !== id) });
}

export function subscribeStore(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

export function useStore<T>(select: (s: State) => T): T {
  return useSyncExternalStore(subscribeStore, () => select(state));
}
