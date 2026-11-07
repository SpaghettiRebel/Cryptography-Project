import { describe, expect, test, vi } from "vitest";
import { api, parseError, subscribe } from "./backend";
import * as W from "./test/wails";

describe("parseError", () => {
  test("строка JSON от Go", () => {
    expect(parseError(W.goError("INVALID_LOGIN", "Логин: правила", "login"))).toEqual({
      code: "INVALID_LOGIN",
      message: "Логин: правила",
      field: "login",
    });
    expect(parseError(W.goError("SERVER_UNAVAILABLE", "Нет связи с сервером"))).toEqual({
      code: "SERVER_UNAVAILABLE",
      message: "Нет связи с сервером",
    });
  });

  test("Error с JSON в сообщении", () => {
    expect(parseError(new Error(W.goError("TIMEOUT"))).code).toBe("TIMEOUT");
  });

  test("всё остальное — INTERNAL", () => {
    for (const e of ["не json", "{}", '{"code":1}', 42, null, undefined, { code: "X" }, new Error("сбой")]) {
      expect(parseError(e).code).toBe("INTERNAL");
    }
  });
});

describe("api и события", () => {
  test("обёртки вызывают привязки Go", async () => {
    await api.login("alice", "password1");
    expect(W.Login).toHaveBeenCalledWith("alice", "password1");
    await api.createChat({ peer: "bob", algorithm: "RC6", keyBits: 256, cipherMode: "CBC", padding: "PKCS7" });
    expect(W.CreateChat).toHaveBeenCalledWith({ peer: "bob", algorithm: "RC6", keyBits: 256, cipherMode: "CBC", padding: "PKCS7" });
  });

  test("подписка получает события и снимается", () => {
    const h = { snapshot: vi.fn(), notice: vi.fn(), sessionEnded: vi.fn() };
    const off = subscribe(h);
    W.emit("snapshot", { version: 1 });
    W.emit("notice", { kind: "info", code: "X", message: "m" });
    W.emit("session-ended", { code: "LOGGED_OUT", login: "a" });
    expect(h.snapshot).toHaveBeenCalledWith({ version: 1 });
    expect(h.notice).toHaveBeenCalledTimes(1);
    expect(h.sessionEnded).toHaveBeenCalledWith({ code: "LOGGED_OUT", login: "a" });
    off();
    W.emit("snapshot", { version: 2 });
    expect(h.snapshot).toHaveBeenCalledTimes(1);
  });
});
