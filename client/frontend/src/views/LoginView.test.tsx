import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { expect, test } from "vitest";
import App from "../App";
import { getState, setState } from "../store";
import * as W from "../test/wails";
import LoginView from "./LoginView";

const type = (testId: string, value: string) => fireEvent.change(screen.getByTestId(testId), { target: { value } });

test("вкладки «Вход» и «Регистрация» и подсказки правил", async () => {
  render(<LoginView />);
  expect(screen.getByRole("tab", { name: "Вход" })).toBeTruthy();
  expect(screen.getByRole("tab", { name: "Регистрация" })).toBeTruthy();
  expect(screen.getByTestId("login-hint").textContent).toContain("3–32");
  expect(screen.getByTestId("password-hint").textContent).toContain("8");
  await waitFor(() => expect(screen.getByTestId("server-line").textContent).toContain("localhost:8443"));
});

test("ошибка с field подсвечивает поле, логин сохраняется", async () => {
  W.Register.mockImplementation(async () => {
    throw W.goError("INVALID_PASSWORD", "Пароль: от 8 до 128 байт", "password");
  });
  render(<LoginView />);
  fireEvent.click(screen.getByRole("tab", { name: "Регистрация" }));
  type("login-input", "alice");
  type("password-input", "short");
  fireEvent.click(screen.getByTestId("submit"));
  await waitFor(() => expect(screen.getByTestId("form-error").textContent).toBe("Пароль: от 8 до 128 байт"));
  expect(screen.getByTestId("password-input").getAttribute("aria-invalid")).toBe("true");
  expect(screen.getByTestId("login-input").getAttribute("aria-invalid")).toBe("false");
  expect((screen.getByTestId("login-input") as HTMLInputElement).value).toBe("alice");
});

test("успешная регистрация переводит на «Вход» с логином", async () => {
  render(<LoginView />);
  fireEvent.click(screen.getByRole("tab", { name: "Регистрация" }));
  type("login-input", "alice");
  type("password-input", "password-1");
  fireEvent.click(screen.getByTestId("submit"));
  await waitFor(() => expect(screen.getByTestId("form-info").textContent).toContain("Учётная запись создана"));
  expect(W.Register).toHaveBeenCalledWith("alice", "password-1");
  expect(screen.getByRole("tab", { name: "Вход" }).getAttribute("aria-selected")).toBe("true");
  expect((screen.getByTestId("login-input") as HTMLInputElement).value).toBe("alice");
  expect((screen.getByTestId("password-input") as HTMLInputElement).value).toBe("");
});

test("вход открывает главное окно", async () => {
  W.Login.mockImplementation(async () => ({ ...W.emptySnapshot, version: 3, connection: "online", session: { login: "alice", userId: "u1" } }));
  render(<LoginView />);
  type("login-input", "alice");
  type("password-input", "password-1");
  fireEvent.click(screen.getByTestId("submit"));
  await waitFor(() => expect(getState().screen).toBe("main"));
  expect(getState().snapshot.session?.login).toBe("alice");
});

test("неверный вход — одно сообщение", async () => {
  W.Login.mockImplementation(async () => {
    throw W.goError("INVALID_CREDENTIALS", "Неверный логин или пароль");
  });
  render(<LoginView />);
  type("login-input", "alice");
  type("password-input", "password-1");
  fireEvent.click(screen.getByTestId("submit"));
  await waitFor(() => expect(screen.getByTestId("form-error").textContent).toBe("Неверный логин или пароль"));
});

test("SESSION_EXPIRED: баннер и заполненный логин", async () => {
  setState({ screen: "main", snapshot: { ...W.emptySnapshot, connection: "online", session: { login: "alice", userId: "u1" } } as never });
  render(<App />);
  W.emit("session-ended", { code: "SESSION_EXPIRED", login: "alice" });
  await waitFor(() => expect(screen.getByTestId("banner").textContent).toContain("Сессия истекла, войдите снова"));
  expect((screen.getByTestId("login-input") as HTMLInputElement).value).toBe("alice");
});

test("недавние логины подставляются", async () => {
  W.GetSettings.mockImplementation(async () => ({ serverAddress: "localhost:8443", certificatePath: "", recentLogins: ["bob", "carol"] }));
  render(<LoginView />);
  await waitFor(() => expect(screen.getByRole("button", { name: "bob" })).toBeTruthy());
  fireEvent.click(screen.getByRole("button", { name: "carol" }));
  expect((screen.getByTestId("login-input") as HTMLInputElement).value).toBe("carol");
});

test("кнопка «Настройки сервера» открывает настройки", () => {
  render(<LoginView />);
  fireEvent.click(screen.getByRole("button", { name: "Настройки сервера" }));
  expect(getState().screen).toBe("settings");
});
