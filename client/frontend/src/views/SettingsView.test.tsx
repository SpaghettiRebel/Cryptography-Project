import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { expect, test } from "vitest";
import App from "../App";
import { getState, setState } from "../store";
import * as W from "../test/wails";
import SettingsView from "./SettingsView";

test("настройки загружаются и сохраняются", async () => {
  render(<SettingsView />);
  await waitFor(() => expect((screen.getByTestId("address-input") as HTMLInputElement).value).toBe("localhost:8443"));
  fireEvent.change(screen.getByTestId("address-input"), { target: { value: "127.0.0.1:18445" } });
  fireEvent.click(screen.getByRole("button", { name: "Сохранить" }));
  await waitFor(() => expect(W.SaveSettings).toHaveBeenCalled());
  expect(W.SaveSettings.mock.calls[0][0]).toMatchObject({ serverAddress: "127.0.0.1:18445", certificatePath: "C:/certs/server.crt" });
  await waitFor(() => expect(getState().screen).toBe("login"));
});

test("ошибка адреса показывается у поля", async () => {
  W.SaveSettings.mockImplementation(async () => {
    throw W.goError("INVALID_ADDRESS", "Адрес сервера: хост:порт");
  });
  render(<SettingsView />);
  await waitFor(() => expect((screen.getByTestId("address-input") as HTMLInputElement).value).toBe("localhost:8443"));
  fireEvent.click(screen.getByRole("button", { name: "Сохранить" }));
  await waitFor(() => expect(screen.getByTestId("settings-error").textContent).toBe("Адрес сервера: хост:порт"));
  expect(screen.getByTestId("address-input").getAttribute("aria-invalid")).toBe("true");
});

test("выбор файла подставляет путь", async () => {
  W.ChooseCertificate.mockImplementation(async () => "D:/новый/server.crt");
  render(<SettingsView />);
  await waitFor(() => expect((screen.getByTestId("address-input") as HTMLInputElement).value).toBe("localhost:8443"));
  fireEvent.click(screen.getByRole("button", { name: "Выбрать файл…" }));
  await waitFor(() => expect((screen.getByTestId("cert-input") as HTMLInputElement).value).toBe("D:/новый/server.crt"));
});

test("«Проверить связь» сохраняет и показывает результат", async () => {
  render(<SettingsView />);
  await waitFor(() => expect((screen.getByTestId("address-input") as HTMLInputElement).value).toBe("localhost:8443"));
  fireEvent.click(screen.getByRole("button", { name: "Проверить связь" }));
  await waitFor(() => expect(screen.getByTestId("check-result").textContent).toBe("Связь с сервером есть"));
  expect(W.SaveSettings).toHaveBeenCalled();
  expect(W.CheckServer).toHaveBeenCalled();

  W.CheckServer.mockImplementation(async () => {
    throw W.goError("SERVER_UNAVAILABLE", "Нет связи с сервером");
  });
  fireEvent.click(screen.getByRole("button", { name: "Проверить связь" }));
  await waitFor(() => expect(screen.getByTestId("settings-error").textContent).toBe("Нет связи с сервером"));
});

test("CERT_INVALID при входе открывает настройки с причиной", async () => {
  W.Login.mockImplementation(async () => {
    throw W.goError("CERT_INVALID", "Сертификат не подходит к серверу");
  });
  setState({ screen: "login" });
  render(<App />);
  fireEvent.change(screen.getByTestId("login-input"), { target: { value: "alice" } });
  fireEvent.change(screen.getByTestId("password-input"), { target: { value: "password-1" } });
  fireEvent.click(screen.getByTestId("submit"));
  await waitFor(() => expect(screen.getByTestId("settings-reason").textContent).toBe("Сертификат не подходит к серверу"));
});
