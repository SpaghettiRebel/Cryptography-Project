import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { expect, test } from "vitest";
import type { Snapshot } from "../backend";
import { setState } from "../store";
import * as W from "../test/wails";
import ContactsTab from "./ContactsTab";

const chat = (id: string, peer: string) => ({
  id, peer, creator: peer, algorithm: "RC6", keyBits: 256, cipherMode: "CBC", padding: "PKCS7", createdAt: 1,
  selfConnected: false, peerConnected: false, keyState: "NONE", algorithmNote: "",
});

function withSnapshot(patch: Partial<Snapshot>) {
  setState({
    screen: "main",
    snapshot: { ...W.emptySnapshot, connection: "online", session: { login: "alice", userId: "u" }, ...patch } as Snapshot,
  });
}

test("три списка и кнопки действий", async () => {
  withSnapshot({ contacts: ["bob"], incoming: ["carol"], outgoing: ["dave"] });
  render(<ContactsTab />);
  const contacts = within(screen.getByTestId("contacts-list"));
  const incoming = within(screen.getByTestId("incoming-list"));
  const outgoing = within(screen.getByTestId("outgoing-list"));
  expect(contacts.getByText("bob")).toBeTruthy();
  expect(contacts.getByRole("button", { name: "Удалить" })).toBeTruthy();
  expect(incoming.getByText("carol")).toBeTruthy();
  fireEvent.click(incoming.getByRole("button", { name: "Принять" }));
  await waitFor(() => expect(W.AcceptContact).toHaveBeenCalledWith("carol"));
  fireEvent.click(incoming.getByRole("button", { name: "Отклонить" }));
  await waitFor(() => expect(W.RejectContact).toHaveBeenCalledWith("carol"));
  expect(outgoing.getByText("dave")).toBeTruthy();
});

test("отправка запроса и ошибка у поля", async () => {
  withSnapshot({});
  W.SendContactRequest.mockImplementationOnce(async () => {
    throw W.goError("USER_NOT_FOUND", "Пользователь не найден");
  });
  render(<ContactsTab />);
  fireEvent.change(screen.getByTestId("contact-input"), { target: { value: "ghost" } });
  fireEvent.click(screen.getByRole("button", { name: "Отправить запрос" }));
  await waitFor(() => expect(screen.getByTestId("contact-error").textContent).toBe("Пользователь не найден"));
  expect(screen.getByTestId("contact-input").getAttribute("aria-invalid")).toBe("true");
  expect((screen.getByTestId("contact-input") as HTMLInputElement).value).toBe("ghost");

  fireEvent.change(screen.getByTestId("contact-input"), { target: { value: "bob" } });
  fireEvent.click(screen.getByRole("button", { name: "Отправить запрос" }));
  await waitFor(() => expect(W.SendContactRequest).toHaveBeenLastCalledWith("bob"));
  await waitFor(() => expect((screen.getByTestId("contact-input") as HTMLInputElement).value).toBe(""));
  expect(screen.queryByTestId("contact-error")).toBeNull();
});

test("подтверждение удаления называет число активных чатов", async () => {
  withSnapshot({ contacts: ["bob", "eve"], chats: [chat("01", "bob"), chat("02", "bob"), chat("03", "eve")] as never });
  render(<ContactsTab />);
  const row = within(screen.getByTestId("contacts-list")).getByText("bob").closest("li")!;
  fireEvent.click(within(row as HTMLElement).getByRole("button", { name: "Удалить" }));
  expect(screen.getByTestId("confirm-text").textContent).toContain("2");
  expect(screen.getByTestId("confirm-text").textContent).toContain("будут закрыты");
  expect(W.RemoveContact).not.toHaveBeenCalled();
  fireEvent.click(screen.getByTestId("confirm-ok"));
  await waitFor(() => expect(W.RemoveContact).toHaveBeenCalledWith("bob"));
  await waitFor(() => expect(screen.queryByTestId("confirm-dialog")).toBeNull());
});

test("удаление контакта без чатов — подтверждение без предупреждения о чатах", () => {
  withSnapshot({ contacts: ["bob"] });
  render(<ContactsTab />);
  fireEvent.click(screen.getByRole("button", { name: "Удалить" }));
  expect(screen.getByTestId("confirm-text").textContent).not.toContain("будут закрыты");
  fireEvent.click(screen.getByRole("button", { name: "Отмена" }));
  expect(screen.queryByTestId("confirm-dialog")).toBeNull();
});
