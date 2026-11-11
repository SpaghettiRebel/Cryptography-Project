import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { CHAT } from "../test/fixtures";
import * as W from "../test/wails";
import ClosedChats from "./ClosedChats";

const closed = { id: CHAT, peer: "carol", algorithm: "MAGENTA", keyBits: 128, cipherMode: "ECB", padding: "ZEROS", createdAt: 1, closedAt: 2, messageCount: 4 };

test("раздел свёрнут, раскрытие загружает закрытые чаты, «История» открывает ленту", async () => {
  W.ListClosedChats.mockResolvedValue([closed]);
  const onHistory = vi.fn();
  render(<ClosedChats onHistory={onHistory} />);
  expect(screen.queryByTestId("closed-item")).toBeNull();
  expect(W.ListClosedChats).not.toHaveBeenCalled();
  fireEvent.click(screen.getByTestId("closed-toggle"));
  await waitFor(() => expect(screen.getByTestId("closed-item").textContent).toContain("carol"));
  expect(screen.getByTestId("closed-count").textContent).toBe("4");
  fireEvent.click(screen.getByTestId("closed-history"));
  expect(onHistory).toHaveBeenCalledWith(closed);
});

test("«Удалить историю» с подтверждением", async () => {
  W.ListClosedChats.mockResolvedValue([closed]);
  render(<ClosedChats onHistory={() => undefined} />);
  fireEvent.click(screen.getByTestId("closed-toggle"));
  await waitFor(() => expect(screen.getByTestId("closed-delete")).toBeTruthy());
  fireEvent.click(screen.getByTestId("closed-delete"));
  expect(screen.getByTestId("confirm-text").textContent).toBe("История и файлы этого чата будут удалены с устройства");
  expect(W.DeleteChatHistory).not.toHaveBeenCalled();
  fireEvent.click(screen.getByTestId("confirm-ok"));
  await waitFor(() => expect(W.DeleteChatHistory).toHaveBeenCalledWith(CHAT));
  await waitFor(() => expect(W.ListClosedChats).toHaveBeenCalledTimes(2));
});
