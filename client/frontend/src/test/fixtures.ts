// Готовые данные для тестов переписки.
import type { ChatView, MessageView, Snapshot } from "../backend";
import { setState } from "../store";
import { emptySnapshot } from "./wails";

export const CHAT = "0123456789abcdef0123456789abcdef";

export const chatView = (patch: Partial<ChatView> = {}): ChatView => ({
  id: CHAT,
  peer: "bob",
  creator: "alice",
  algorithm: "RC6",
  keyBits: 256,
  cipherMode: "CBC",
  padding: "PKCS7",
  createdAt: Date.UTC(2026, 10, 7, 10, 30),
  selfConnected: true,
  peerConnected: true,
  keyState: "READY",
  algorithmNote: "",
  unread: 0,
  receiveState: "RECEIVING",
  receiveReason: "",
  canSend: true,
  sendBlock: "",
  ...patch,
});

export const msg = (patch: Partial<MessageView> = {}): MessageView => ({
  seq: 1,
  chatId: CHAT,
  direction: "in",
  kind: "text",
  createdAt: Date.UTC(2026, 10, 7, 11, 0),
  state: "DONE",
  text: "привет",
  fileName: "",
  size: 12,
  mediaUrl: "",
  error: null,
  encrypt: null,
  send: null,
  receive: null,
  decrypt: null,
  cancellable: false,
  retryable: false,
  ...patch,
});

export function withChats(chats: ChatView[], patch: Partial<Snapshot> = {}) {
  setState({
    screen: "main",
    snapshot: { ...emptySnapshot, connection: "online", session: { login: "alice", userId: "u" }, chats, ...patch } as Snapshot,
  });
}
