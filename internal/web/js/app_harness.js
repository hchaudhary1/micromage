const appPath = "../../../cmd/server/web/static/app.js";
const appStatePath = "../../../cmd/server/web/static/app_state.js";

function makeElement(tagName = "div") {
  const element = {
    children: [],
    className: "",
    dataset: {},
    disabled: false,
    innerHTML: "",
    listeners: {},
    scrollHeight: 0,
    scrollLeft: 0,
    scrollTop: 0,
    tagName,
    textContent: "",
    value: "",
    classList: {
      add(name) {
        if (!element.className.split(/\s+/).includes(name)) {
          element.className = `${element.className} ${name}`.trim();
        }
      },
      remove(name) {
        element.className = element.className
          .split(/\s+/)
          .filter((item) => item && item !== name)
          .join(" ");
      },
    },
    addEventListener(type, listener) {
      this.listeners[type] = listener;
    },
    appendChild(child) {
      this.children.push(child);
      return child;
    },
    closest() {
      return null;
    },
    prepend(child) {
      this.children.unshift(child);
      return child;
    },
    replaceChildren(...children) {
      this.children = children;
    },
    setAttribute(name, value) {
      this[name] = value;
      if (name.startsWith("data-")) {
        const key = name
          .slice(5)
          .replace(/-([a-z])/g, (_, letter) => letter.toUpperCase());
        this.dataset[key] = value;
      }
    },
  };
  return element;
}

function appElements() {
  return {
    "#template-select": makeElement("select"),
    "#yaml-editor": makeElement("textarea"),
    "#preview-state": makeElement("span"),
    "#run-button": makeElement("button"),
    "#cancel-run-button": makeElement("button"),
    "#run-arguments": makeElement("textarea"),
    "#run-mode": makeElement("select"),
    "#workflow-summary": makeElement("div"),
    "#workflow-name": makeElement("h2"),
    "#workflow-description": makeElement("p"),
    "#run-status": makeElement("p"),
    "#issue-counts": makeElement("div"),
    "#issue-panel": makeElement("div"),
    ".graph-wrap": makeElement("div"),
    "#dag-svg": makeElement("svg"),
    "#fit-graph-button": makeElement("button"),
    "#inspector-body": makeElement("div"),
    "#run-log": makeElement("div"),
  };
}

function response({ ok = true, status = 200, statusText = "OK", contentType = "application/json", body = "" }) {
  return {
    ok,
    status,
    statusText,
    headers: {
      get(name) {
        return name.toLowerCase() === "content-type" ? contentType : "";
      },
    },
    json: async () => body,
    text: async () => body,
  };
}

function streamResponse(events) {
  const encoder = new TextEncoder();
  const chunks = events.map((event) => encoder.encode(`data: ${JSON.stringify(event)}\n\n`));
  return {
    ok: true,
    status: 200,
    statusText: "OK",
    headers: { get: () => "text/event-stream" },
    body: {
      getReader() {
        let index = 0;
        return {
          async read() {
            if (index >= chunks.length) {
              return { done: true, value: undefined };
            }
            return { done: false, value: chunks[index++] };
          },
        };
      },
    },
  };
}

async function flushAsyncWork() {
  await new Promise((resolve) => setImmediate(resolve));
  await new Promise((resolve) => setImmediate(resolve));
}

async function bootHarness(options = {}) {
  delete require.cache[require.resolve(appPath)];
  delete require.cache[require.resolve(appStatePath)];

  const elements = appElements();
  const templates = options.templates || [{ id: "starter", name: "Starter", yaml: "name: starter\nnodes: []\n" }];
  const requests = [];
  const confirms = [];
  const timers = [];
  const intervals = [];

  elements["#run-mode"].value = options.mode || "simulate";
  elements["#run-arguments"].value = options.arguments || "";

  // Browser workflow tests need UI-observable behavior without adding a DOM package dependency.
  global.document = {
    querySelector: (selector) => elements[selector],
    createElement: (tagName) => makeElement(tagName),
    createElementNS: (_namespace, tagName) => makeElement(tagName),
  };
  global.window = {
    clearTimeout: () => {},
    confirm: (message) => {
      confirms.push(message);
      if (typeof options.confirm === "function") {
        return options.confirm(message);
      }
      if (Object.hasOwn(options, "confirm")) {
        return options.confirm;
      }
      throw new Error("unexpected confirm");
    },
    localStorage: { getItem: () => "" },
    clearInterval: (timer) => {
      intervals[timer - 1] = null;
    },
    setTimeout: (callback) => {
      timers.push(callback);
      return timers.length;
    },
    setInterval: (callback) => {
      intervals.push(callback);
      return intervals.length;
    },
  };
  window.MicromageTemplateState = require(appStatePath);
  global.fetch = async (url, requestOptions = {}) => {
    const body = requestOptions.body ? JSON.parse(requestOptions.body) : null;
    requests.push({ url, body, headers: requestOptions.headers || {}, signal: requestOptions.signal });
    if (url === "/api/templates") {
      return response({ body: templates });
    }
    if (url === "/api/preview") {
      const preview = options.previewFor ? options.previewFor(body) : options.preview || runnablePreview();
      return response({ body: preview });
    }
    if (url === "/api/run") {
      if (options.runResponseFor) {
        return options.runResponseFor(body, requestOptions);
      }
      return options.runResponse || streamResponse([]);
    }
    throw new Error(`unexpected fetch ${url}`);
  };

  require(appPath);
  await flushAsyncWork();

  async function runPendingTimers() {
    while (timers.length) {
      const callback = timers.shift();
      await callback();
      await flushAsyncWork();
    }
    await flushAsyncWork();
  }

  return { confirms, elements, intervals, requests, runPendingTimers };
}

function runnablePreview(overrides = {}) {
  return {
    can_run: true,
    graph: { edges: [], height: 0, nodes: [], width: 0 },
    issues: [],
    workflow: { name: "Starter", description: "" },
    ...overrides,
  };
}

function logText(elements) {
  return elements["#run-log"].children.map((child) => child.textContent).join("\n");
}

module.exports = {
  bootHarness,
  flushAsyncWork,
  logText,
  response,
  runnablePreview,
  streamResponse,
};
