import { mount } from "svelte";
import App from "./App.svelte";
import "./style.css";

const target = document.getElementById("app");
if (!target) throw new Error("Docket mount element is missing");
mount(App, { target });
