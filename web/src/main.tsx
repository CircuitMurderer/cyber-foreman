import {StrictMode} from "react";
import {createRoot} from "react-dom/client";
import "./styles.css";
import {App} from "./App";

const systemTheme = window.matchMedia("(prefers-color-scheme: dark)");
const syncTheme = () => {
  document.documentElement.dataset.theme = systemTheme.matches ? "dark" : "light";
};
syncTheme();
systemTheme.addEventListener("change", syncTheme);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>
);
