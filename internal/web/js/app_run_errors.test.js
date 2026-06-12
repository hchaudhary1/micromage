const assert = require("node:assert/strict");

const appPath = "../../../cmd/server/web/static/app.js";
const appStatePath = "../../../cmd/server/web/static/app_state.js";

function makeElement() {
  const element = {
    children: [],
    className: "",
    disabled: false,
    innerHTML: "",
    listeners: {},
    scrollHeight: 0,
    scrollTop: 0,
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
    prepend(child) {
      this.children.unshift(child);
      return child;
    },
    replaceChildren(...children) {
      this.children = children;
    },
    setAttribute(name, value) {
      this[name] = value;
    },
  };
  return element;
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

async function flushAsyncWork() {
  await new Promise((resolve) => setImmediate(resolve));
  await new Promise((resolve) => setImmediate(resolve));
}

async function bootHarness(options) {
  delete require.cache[require.resolve(appPath)];
  delete require.cache[require.resolve(appStatePath)];

  const elements = {
    "#template-select": makeElement(),
    "#yaml-editor": makeElement(),
    "#preview-state": makeElement(),
    "#run-button": makeElement(),
    "#cancel-run-button": makeElement(),
    "#run-arguments": makeElement(),
    "#run-mode": makeElement(),
    "#workflow-summary": makeElement(),
    "#workflow-name": makeElement(),
    "#workflow-description": makeElement(),
    "#run-status": makeElement(),
    "#issue-counts": makeElement(),
    "#issue-panel": makeElement(),
    ".graph-wrap": makeElement(),
    "#dag-svg": makeElement(),
    "#fit-graph-button": makeElement(),
    "#inspector-body": makeElement(),
    "#run-log": makeElement(),
  };
  elements["#run-mode"].value = options.mode || "simulate";

  const templates = [{ id: "starter", name: "Starter", yaml: options.yaml || "name: starter\nnodes: []\n" }];
  const preview = options.preview || runnablePreview();
  const requests = [];
  const confirms = [];

  global.document = {
    querySelector: (selector) => elements[selector],
    createElement: () => makeElement(),
    createElementNS: () => makeElement(),
  };
  global.window = {
    clearTimeout,
    confirm: (message) => {
      confirms.push(message);
      if (Object.hasOwn(options, "confirm")) {
        return options.confirm;
      }
      throw new Error("unexpected confirm");
    },
    clearInterval,
    localStorage: { getItem: () => "" },
    setInterval,
    setTimeout,
  };
  window.MicromageTemplateState = require(appStatePath);
  global.fetch = async (url, requestOptions = {}) => {
    requests.push({ url, body: requestOptions.body ? JSON.parse(requestOptions.body) : null, signal: requestOptions.signal });
    if (url === "/api/templates") {
      return response({ body: templates });
    }
    if (url === "/api/preview") {
      return response({ body: preview });
    }
    if (url === "/api/run") {
      return options.runResponse;
    }
    throw new Error(`unexpected fetch ${url}`);
  };

  require(appPath);
  await flushAsyncWork();
  return { confirms, elements, requests };
}

function runnablePreview() {
  return {
    can_run: true,
    graph: { edges: [], height: 0, nodes: [], width: 0 },
    issues: [],
    workflow: { name: "Starter", description: "" },
  };
}

function runnableRealPreview() {
  return {
    can_run: true,
    graph: {
      edges: [],
      height: 0,
      nodes: [
        { id: "plan", type: "prompt", label: "Prompt", summary: "Plan", metadata: { provider: "openai", model: "gpt-5" } },
        { id: "setup", type: "bash", label: "Shell", summary: "echo setup", metadata: {} },
        { id: "review", type: "command", label: "micromage-code-review-agent", summary: "micromage-code-review-agent", metadata: { provider: "opencode", model: "opencode/nemotron-3-ultra-free" } },
      ],
      width: 0,
    },
    issues: [],
    workflow: {
      name: "Real Review",
      description: "",
      provider: "opencode",
      model: "opencode/nemotron-3-ultra-free",
      nodes: [
        { id: "plan", prompt: "Plan", provider: "openai", model: "gpt-5" },
        { id: "setup", bash: "echo setup" },
        { id: "review", command: "micromage-code-review-agent" },
      ],
    },
  };
}

function logText(elements) {
  return elements["#run-log"].children.map((child) => child.textContent).join("\n");
}

async function testInvalidYAMLRendersGlobalWorkflowIssue() {
  const { elements } = await bootHarness({
    preview: {
      can_run: false,
      graph: { edges: null, height: 0, nodes: null, width: 0 },
      issues: [{ level: "error", field: "yaml", message: "invalid YAML: did not find expected node content" }],
      workflow: {},
    },
  });

  assert.match(elements["#issue-counts"].innerHTML, /1 errors/);
  assert.match(elements["#issue-panel"].innerHTML, /Workflow \/ yaml/);
  assert.match(elements["#issue-panel"].innerHTML, /invalid YAML/);
  assert.equal(elements["#run-button"].disabled, true);
  assert.equal(elements["#preview-state"].textContent, "Invalid");
}

async function testDisabledRealModeShowsServerDetail() {
  const { elements } = await bootHarness({
    mode: "real",
    confirm: true,
    runResponse: response({
      ok: false,
      status: 403,
      statusText: "Forbidden",
      contentType: "text/plain; charset=utf-8",
      body: "real runs require MICROMAGE_ENABLE_REAL_RUNS=1\n",
    }),
  });

  await elements["#run-button"].listeners.click();

  assert.match(logText(elements), /run rejected: real runs require MICROMAGE_ENABLE_REAL_RUNS=1/);
}

async function testRealModeCancelStopsBeforeRunRequest() {
  const { confirms, elements, requests } = await bootHarness({
    mode: "real",
    preview: runnableRealPreview(),
    confirm: false,
  });

  await elements["#run-button"].listeners.click();

  assert.equal(confirms.length, 1);
  assert.ok(!requests.some((request) => request.url === "/api/run"));
  assert.match(logText(elements), /real run canceled/);
  assert.equal(elements["#run-button"].disabled, false);
}

async function testRealModeConfirmationSummarizesExecutionRisk() {
  const { confirms, elements } = await bootHarness({
    mode: "real",
    preview: runnableRealPreview(),
    confirm: true,
    runResponse: response({
      ok: false,
      status: 403,
      statusText: "Forbidden",
      contentType: "text/plain; charset=utf-8",
      body: "real runs require MICROMAGE_ENABLE_REAL_RUNS=1\n",
    }),
  });

  await elements["#run-button"].listeners.click();

  assert.equal(confirms.length, 1);
  assert.match(confirms[0], /Real run preflight/);
  assert.match(confirms[0], /Executable node kinds: prompt 1, bash 1, command 1/);
  assert.match(confirms[0], /Bash\/command nodes: 2 \(bash 1, command 1\)/);
  assert.match(confirms[0], /Workflow provider\/model: opencode \/ opencode\/nemotron-3-ultra-free/);
  assert.match(confirms[0], /Node provider\/model overrides: plan: openai \/ gpt-5/);
  assert.match(confirms[0], /Artifacts: real runs create a repo-local \.micromage\/runs\/<run-id> directory/);
  assert.match(confirms[0], /Risks: may execute shell commands, send prompts to providers, write artifacts, and modify repository files/);
}

async function testInvalidModeShowsServerDetail() {
  const { elements } = await bootHarness({
    mode: "invalid",
    runResponse: response({
      ok: false,
      status: 400,
      statusText: "Bad Request",
      contentType: "text/plain; charset=utf-8",
      body: "invalid run mode\n",
    }),
  });

  await elements["#run-button"].listeners.click();

  assert.match(logText(elements), /run rejected: invalid run mode/);
}

async function testValidationRejectionRendersPreviewIssues() {
  const rejectedPreview = {
    can_run: false,
    graph: {
      edges: [],
      height: 120,
      nodes: [
        {
          id: "plan",
          type: "prompt",
          label: "Plan",
          summary: "Plan",
          x: 0,
          y: 0,
          issues: [{ level: "error", node_id: "plan", field: "depends_on", message: "dependency missing was not found" }],
        },
      ],
      width: 220,
    },
    issues: [
      { level: "error", field: "provider", message: "provider codex is not registered for real mode" },
      { level: "error", node_id: "plan", field: "depends_on", message: "dependency missing was not found" },
    ],
    workflow: { name: "Rejected", description: "" },
  };
  const { elements } = await bootHarness({
    runResponse: response({
      ok: false,
      status: 400,
      statusText: "Bad Request",
      body: rejectedPreview,
    }),
  });

  await elements["#run-button"].listeners.click();

  assert.match(elements["#issue-panel"].innerHTML, /Workflow \/ provider/);
  assert.match(elements["#issue-panel"].innerHTML, /Node plan \/ depends_on/);
  assert.match(logText(elements), /run rejected: validation failed/);
  assert.match(logText(elements), /Workflow \/ provider: provider codex is not registered/);
  assert.match(logText(elements), /Node plan \/ depends_on: dependency missing was not found/);
  assert.equal(elements["#run-button"].disabled, true);
}

async function main() {
  await testInvalidYAMLRendersGlobalWorkflowIssue();
  await testDisabledRealModeShowsServerDetail();
  await testRealModeCancelStopsBeforeRunRequest();
  await testRealModeConfirmationSummarizesExecutionRisk();
  await testInvalidModeShowsServerDetail();
  await testValidationRejectionRendersPreviewIssues();
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
