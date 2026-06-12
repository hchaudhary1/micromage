const assert = require("node:assert/strict");
const { bootHarness, logText, response, runnablePreview, streamResponse } = require("./app_harness.js");

const templates = [
  {
    id: "starter",
    name: "Starter",
    yaml: "name: starter\nnodes:\n  - id: plan\n    prompt: Plan\n",
  },
  {
    id: "review",
    name: "Review",
    yaml: "name: review\nnodes:\n  - id: inspect\n    prompt: Inspect\n",
  },
];

function previewFor(request) {
  if (request.yaml.includes("bad: [")) {
    return {
      can_run: false,
      graph: { edges: null, height: 0, nodes: null, width: 0 },
      issues: [{ level: "error", field: "yaml", message: "invalid YAML: did not find expected ',' or ']'" }],
      workflow: {},
    };
  }
  if (request.yaml.includes("review")) {
    return workflowPreview({
      name: "Review",
      description: "Review workflow",
      nodes: [
        {
          id: "inspect",
          type: "prompt",
          label: "Inspect",
          summary: "Inspect changes",
          metadata: { provider: "openai", model: "gpt-5" },
          x: 24,
          y: 32,
        },
      ],
    });
  }
  return workflowPreview({
    name: "Starter",
    description: "Starter workflow",
    nodes: [
      {
        id: "plan",
        type: "prompt",
        label: "Plan",
        summary: "Plan the work",
        metadata: { provider: "openai", model: "gpt-5-mini" },
        x: 24,
        y: 32,
      },
    ],
  });
}

function workflowPreview(workflow) {
  return runnablePreview({
    graph: { edges: [], height: 180, nodes: workflow.nodes, width: 260 },
    workflow,
  });
}

async function testTemplateManualEditValidationAndNodeInspection() {
  let promptCount = 0;
  const { elements, requests, runPendingTimers } = await bootHarness({
    templates,
    previewFor,
    confirm: () => {
      promptCount += 1;
      return true;
    },
  });

  assert.equal(elements["#template-select"].value, "starter");
  assert.equal(elements["#yaml-editor"].value, templates[0].yaml);
  assert.equal(elements["#workflow-name"].textContent, "Starter");
  assert.equal(elements["#workflow-summary"].textContent, "1 nodes");

  elements["#template-select"].value = "review";
  elements["#template-select"].listeners.change();
  await runPendingTimers();

  assert.equal(promptCount, 0);
  assert.equal(elements["#yaml-editor"].value, templates[1].yaml);
  assert.equal(elements["#workflow-name"].textContent, "Review");
  assert.equal(requests.at(-1).body.yaml, templates[1].yaml);

  elements["#yaml-editor"].value = `${templates[1].yaml}bad: [\n`;
  elements["#yaml-editor"].listeners.input();
  await runPendingTimers();

  assert.equal(elements["#preview-state"].textContent, "Invalid");
  assert.equal(elements["#run-button"].disabled, true);
  assert.match(elements["#issue-panel"].innerHTML, /Workflow \/ yaml/);
  assert.match(elements["#issue-panel"].innerHTML, /invalid YAML/);
  assert.match(requests.at(-1).body.yaml, /bad: \[/);

  elements["#yaml-editor"].value = `${templates[1].yaml}description: Manual review\n`;
  elements["#yaml-editor"].listeners.input();
  await runPendingTimers();

  assert.equal(elements["#preview-state"].textContent, "Runnable");
  assert.equal(elements["#run-button"].disabled, false);
  assert.equal(elements["#inspector-body"].textContent, "Select a node");

  const inspectNode = elements["#dag-svg"].children.find((child) => child.class?.includes("dag-node"));
  inspectNode.listeners.click();

  assert.match(elements["#inspector-body"].innerHTML, /provider/);
  assert.match(elements["#inspector-body"].innerHTML, /openai/);
  assert.match(elements["#inspector-body"].innerHTML, /gpt-5/);
}

async function testSimulateRunStreamsLogAndSummary() {
  const { elements, requests } = await bootHarness({
    templates,
    previewFor,
    runResponse: streamResponse([
      { type: "workflow_start", message: "workflow started" },
      { type: "node_complete", node_id: "plan", message: "plan complete" },
      {
        type: "run_summary",
        message: "run summary ready",
        run_id: "run-123",
        artifacts_dir: ".micromage/runs/run-123",
        completed_nodes: ["plan"],
        failed_nodes: [],
        artifacts: [{ node_id: "plan", path: ".micromage/runs/run-123/plan.md" }],
      },
    ]),
  });

  elements["#run-mode"].value = "simulate";
  await elements["#run-button"].listeners.click();

  const runRequest = requests.find((request) => request.url === "/api/run");
  assert.equal(runRequest.body.mode, "simulate");
  assert.match(logText(elements), /opening run stream/);
  assert.match(logText(elements), /workflow started/);
  assert.match(logText(elements), /Run ID: run-123/);
  assert.match(logText(elements), /Completed nodes: plan/);
  assert.match(logText(elements), /Artifact plan: \.micromage\/runs\/run-123\/plan\.md/);
  assert.equal(elements["#run-button"].disabled, false);
}

async function testRunModeSelectionAndRealModeRejection() {
  const { confirms, elements, requests, runPendingTimers } = await bootHarness({
    templates,
    previewFor,
    confirm: true,
    runResponse: response({
      ok: false,
      status: 403,
      statusText: "Forbidden",
      contentType: "text/plain; charset=utf-8",
      body: "real runs require MICROMAGE_ENABLE_REAL_RUNS=1\n",
    }),
  });

  elements["#run-mode"].value = "real";
  elements["#run-mode"].listeners.change();
  await runPendingTimers();

  assert.equal(requests.at(-1).url, "/api/preview");
  assert.equal(requests.at(-1).body.mode, "real");

  await elements["#run-button"].listeners.click();

  const runRequest = requests.find((request) => request.url === "/api/run");
  assert.equal(confirms.length, 1);
  assert.match(confirms[0], /Real run preflight/);
  assert.equal(runRequest.body.mode, "real");
  assert.match(logText(elements), /run rejected: real runs require MICROMAGE_ENABLE_REAL_RUNS=1/);
  assert.equal(elements["#run-button"].disabled, false);
}

async function main() {
  await testTemplateManualEditValidationAndNodeInspection();
  await testSimulateRunStreamsLogAndSummary();
  await testRunModeSelectionAndRealModeRejection();
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
