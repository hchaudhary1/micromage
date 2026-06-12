const assert = require("node:assert/strict");

const templates = [
  { id: "starter", name: "Starter", yaml: "name: starter\nnodes: []\n" },
  { id: "review", name: "Review", yaml: "name: review\nnodes: []\n" },
];

function makeElement() {
  return {
    children: [],
    classList: {
      add() {},
      remove() {},
    },
    listeners: {},
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
}

async function flushAsyncWork() {
  await new Promise((resolve) => setImmediate(resolve));
  await new Promise((resolve) => setImmediate(resolve));
}

async function main() {
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
    "#definition-kind": makeElement(),
    "#definition-id": makeElement(),
    "#save-definition-button": makeElement(),
    "#refresh-persistence-button": makeElement(),
    "#cleanup-preview-button": makeElement(),
    "#definition-list": makeElement(),
    "#run-history-list": makeElement(),
    "#cleanup-preview": makeElement(),
    "#run-detail": makeElement(),
  };
  elements["#run-mode"].value = "simulate";
  elements["#definition-kind"].value = "workflow";

  const previewRequests = [];
  global.document = {
    querySelector: (selector) => elements[selector],
    createElement: () => makeElement(),
    createElementNS: () => makeElement(),
  };
  global.window = {
    clearTimeout,
    confirm: () => {
      throw new Error("unexpected confirm");
    },
    clearInterval,
    localStorage: { getItem: () => "" },
    setInterval,
    setTimeout,
  };
  window.MicromageTemplateState = require("../../../cmd/server/web/static/app_state.js");
  global.fetch = async (url, options = {}) => {
    if (url === "/api/templates") {
      return { json: async () => templates };
    }
    if (url === "/api/preview") {
      previewRequests.push(JSON.parse(options.body));
      return {
        json: async () => ({
          can_run: true,
          graph: { edges: [], height: 0, nodes: [], width: 0 },
          issues: [],
          workflow: {},
        }),
      };
    }
    if (url === "/api/runs") {
      return { ok: true, json: async () => [] };
    }
    if (url === "/api/runs/cleanup/preview") {
      return { ok: true, json: async () => ({ dry_run: true, candidates: [], deleted: [], failed: [] }) };
    }
    if (url === "/api/definitions") {
      const body = JSON.parse(options.body);
      return { ok: true, json: async () => ({ id: body.id, name: body.id, yaml: body.yaml, source: "project", kind: body.kind }) };
    }
    throw new Error(`unexpected fetch ${url}`);
  };

  require("../../../cmd/server/web/static/app.js");
  await flushAsyncWork();

  assert.equal(elements["#template-select"].value, "starter");
  assert.equal(elements["#yaml-editor"].value, templates[0].yaml);
  assert.equal(previewRequests.length, 1);

  elements["#template-select"].value = "review";
  elements["#template-select"].listeners.change();
  await flushAsyncWork();

  assert.equal(elements["#yaml-editor"].value, templates[1].yaml);
  assert.equal(previewRequests.length, 2);

  let prompts = 0;
  const editedYaml = templates[1].yaml + "# manual edit\n";
  elements["#yaml-editor"].value = editedYaml;
  window.confirm = () => {
    prompts += 1;
    return false;
  };
  elements["#template-select"].value = "starter";
  elements["#template-select"].listeners.change();
  await flushAsyncWork();

  assert.equal(prompts, 1);
  assert.equal(elements["#template-select"].value, "review");
  assert.equal(elements["#yaml-editor"].value, editedYaml);
  assert.equal(previewRequests.length, 2);

  window.confirm = () => {
    prompts += 1;
    return true;
  };
  elements["#template-select"].value = "starter";
  elements["#template-select"].listeners.change();
  await flushAsyncWork();

  assert.equal(prompts, 2);
  assert.equal(elements["#template-select"].value, "starter");
  assert.equal(elements["#yaml-editor"].value, templates[0].yaml);
  assert.equal(previewRequests.length, 3);
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
