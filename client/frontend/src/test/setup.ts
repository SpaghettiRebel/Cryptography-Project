import { beforeEach } from "vitest";
import { resetFeeds } from "../messages";
import { resetStore } from "../store";
import { resetWails } from "./wails";

beforeEach(() => {
  resetWails();
  resetStore();
  resetFeeds();
});
