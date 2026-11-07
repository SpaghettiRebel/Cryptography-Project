import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import * as W from "../test/wails";
import CreateChatDialog from "./CreateChatDialog";

const devOptions = {
  ...W.defaultOptions,
  algorithms: [
    { id: "MAGENTA", available: true, substitute: true, note: "тестовый шифр вместо MAGENTA, только для разработки" },
    { id: "RC6", available: true, substitute: true, note: "тестовый шифр вместо RC6, только для разработки" },
  ],
};

const mixedOptions = {
  ...W.defaultOptions,
  algorithms: [
    { id: "MAGENTA", available: false, substitute: false, note: "алгоритм не реализован в этой сборке" },
    { id: "RC6", available: true, substitute: false, note: "" },
  ],
};

const select = (id: string) => screen.getByTestId(id) as HTMLSelectElement;

test("недоступный алгоритм неактивен с пояснением, по умолчанию — первый доступный", async () => {
  W.ChatOptions.mockImplementation(async () => mixedOptions);
  render(<CreateChatDialog contacts={["bob"]} onClose={() => undefined} onOpenContacts={() => undefined} />);
  await waitFor(() => expect(select("algorithm-select").value).toBe("RC6"));
  const magenta = screen.getByRole("option", { name: /MAGENTA/ }) as HTMLOptionElement;
  expect(magenta.disabled).toBe(true);
  expect(magenta.title).toBe("алгоритм не реализован в этой сборке");
  expect(screen.getByTestId("algorithm-notes").textContent).toContain("MAGENTA: алгоритм не реализован в этой сборке");
  expect(select("keybits-select").value).toBe("256");
  expect(select("mode-select").value).toBe("CBC");
  expect(select("padding-select").value).toBe("PKCS7");
});

test("пометка подстановки тестового шифра", async () => {
  W.ChatOptions.mockImplementation(async () => devOptions);
  render(<CreateChatDialog contacts={["bob"]} onClose={() => undefined} onOpenContacts={() => undefined} />);
  await waitFor(() => expect(select("algorithm-select").value).toBe("MAGENTA"));
  expect(screen.getByTestId("substitute-note").textContent).toContain("тестовый шифр вместо MAGENTA");
});

test("предупреждения ECB и Zeros не блокируют создание", async () => {
  W.ChatOptions.mockImplementation(async () => devOptions);
  W.CreateChat.mockImplementation(async () => ({ id: "a".repeat(32) }));
  const close = vi.fn();
  render(<CreateChatDialog contacts={["bob"]} onClose={close} onOpenContacts={() => undefined} />);
  await waitFor(() => expect(select("algorithm-select").value).toBe("MAGENTA"));
  fireEvent.change(select("mode-select"), { target: { value: "ECB" } });
  fireEvent.change(select("padding-select"), { target: { value: "ZEROS" } });
  expect(screen.getByTestId("mode-warning").textContent).toBe("ECB раскрывает повторы");
  expect(screen.getByTestId("padding-warning").textContent).toBe("Zeros не отличает нули от набивки");
  fireEvent.change(select("keybits-select"), { target: { value: "128" } });
  fireEvent.click(screen.getByRole("button", { name: "Создать" }));
  await waitFor(() =>
    expect(W.CreateChat).toHaveBeenCalledWith({ peer: "bob", algorithm: "MAGENTA", keyBits: 128, cipherMode: "ECB", padding: "ZEROS" }),
  );
  await waitFor(() => expect(close).toHaveBeenCalled());
});

test("без доступных алгоритмов кнопка «Создать» неактивна с пояснением", async () => {
  render(<CreateChatDialog contacts={["bob"]} onClose={() => undefined} onOpenContacts={() => undefined} />);
  await waitFor(() => expect(screen.getByTestId("no-algorithms")).toBeTruthy());
  expect((screen.getByRole("button", { name: "Создать" }) as HTMLButtonElement).disabled).toBe(true);
  expect(screen.getByTestId("no-algorithms").textContent).toContain("нет доступных алгоритмов");
});

test("ошибка сервера показывается в диалоге с подсветкой поля", async () => {
  W.ChatOptions.mockImplementation(async () => devOptions);
  W.CreateChat.mockImplementation(async () => {
    throw W.goError("NOT_CONTACT", "Пользователь больше не ваш контакт");
  });
  render(<CreateChatDialog contacts={["bob"]} onClose={() => undefined} onOpenContacts={() => undefined} />);
  await waitFor(() => expect(select("algorithm-select").value).toBe("MAGENTA"));
  fireEvent.click(screen.getByRole("button", { name: "Создать" }));
  await waitFor(() => expect(screen.getByTestId("create-error").textContent).toBe("Пользователь больше не ваш контакт"));
  expect(select("peer-select").getAttribute("aria-invalid")).toBe("true");
});

test("сбой получения параметров: ошибка вместо вечной загрузки, диалог закрывается", async () => {
  W.ChatOptions.mockImplementation(async () => {
    throw new Error(JSON.stringify({ code: "INTERNAL", message: "Внутренняя ошибка клиента" }));
  });
  const close = vi.fn();
  render(<CreateChatDialog contacts={["bob"]} onClose={close} onOpenContacts={() => undefined} />);
  await waitFor(() => expect(screen.getByTestId("options-error").textContent).toContain("Внутренняя ошибка клиента"));
  expect(screen.queryByText("Загрузка…")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Закрыть" }));
  expect(close).toHaveBeenCalled();
});
