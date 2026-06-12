const assert = require("node:assert/strict");
const { bootHarness, flushAsyncWork, logText, response, runnablePreview, streamResponse } = require("./app_harness.js");

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

function largeWorkflowPreview() {
  return runnablePreview({
    graph: {
      edges: [{ source: "plan", target: "verify" }],
      height: 220,
      width: 1680,
      nodes: [
        {
          id: "plan",
          type: "prompt",
          label: "Plan",
          summary: "Plan the work",
          metadata: {},
          x: 24,
          y: 32,
        },
        {
          id: "verify",
          type: "bash",
          label: "Verify",
          summary: "go test ./...",
          metadata: {},
          x: 1420,
          y: 32,
        },
      ],
    },
    workflow: { name: "Large", description: "Wide graph" },
  });
}

function graphNode(elements, nodeID) {
  return elements["#dag-svg"].children.find(
    (child) => child.class?.includes("dag-node") && child["aria-label"]?.startsWith(`${nodeID} `),
  );
}

function abortError() {
  const error = new Error("aborted");
  error.name = "AbortError";
  return error;
}

function cancellableStreamResponse(signal, events) {
  const encoder = new TextEncoder();
  const chunks = events.map((event) => encoder.encode(`data: ${JSON.stringify(event)}\n\n`));
  let index = 0;
  let rejectPendingRead = null;
  signal.addEventListener("abort", () => {
    if (rejectPendingRead) {
      rejectPendingRead(abortError());
    }
  });
  return {
    ok: true,
    status: 200,
    statusText: "OK",
    headers: { get: () => "text/event-stream" },
    body: {
      getReader() {
        return {
          async read() {
            if (index < chunks.length) {
              return { done: false, value: chunks[index++] };
            }
            return new Promise((_resolve, reject) => {
              rejectPendingRead = reject;
              if (signal.aborted) {
                reject(abortError());
              }
            });
          },
        };
      },
    },
  };
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

async function testGraphFitButtonTogglesActualAndFitSizing() {
  const { elements } = await bootHarness({
    templates,
    preview: largeWorkflowPreview(),
  });

  assert.equal(elements["#dag-svg"].width, "1680");
  assert.equal(elements["#dag-svg"]["data-fit"], "actual");
  assert.equal(elements["#fit-graph-button"].textContent, "Fit");

  elements["#fit-graph-button"].listeners.click();

  assert.equal(elements["#dag-svg"].width, "100%");
  assert.equal(elements["#dag-svg"].height, "100%");
  assert.equal(elements["#dag-svg"]["data-fit"], "fit");
  assert.match(elements[".graph-wrap"].className, /fit-to-screen/);
  assert.equal(elements["#fit-graph-button"].textContent, "Actual size");
}

async function testRunStateOverlaysAndStatusContext() {
  const { elements } = await bootHarness({
    templates,
    preview: largeWorkflowPreview(),
    runResponse: streamResponse([
      { type: "workflow_start", message: "workflow started" },
      { type: "node_start", node_id: "plan", message: "starting plan" },
      { type: "node_complete", node_id: "plan", message: "plan complete" },
      { type: "node_start", node_id: "verify", message: "starting verify" },
      { type: "node_failed", node_id: "verify", message: "verify failed" },
      { type: "workflow_failed", message: "workflow failed" },
    ]),
  });

  await elements["#run-button"].listeners.click();

  assert.match(graphNode(elements, "plan").class, /run-state-succeeded/);
  assert.match(graphNode(elements, "verify").class, /run-state-failed/);
  assert.match(elements["#run-status"].textContent, /^Failed in 00:00/);
  assert.match(logText(elements), /verify failed/);
}

async function testCancelRunAbortsOpenStream() {
  const { elements, requests } = await bootHarness({
    templates,
    preview: largeWorkflowPreview(),
    runResponseFor: (_body, requestOptions) =>
      cancellableStreamResponse(requestOptions.signal, [
        { type: "workflow_start", message: "workflow started" },
        { type: "node_start", node_id: "plan", message: "starting plan" },
      ]),
  });

  const runPromise = elements["#run-button"].listeners.click();
  await flushAsyncWork();

  assert.match(graphNode(elements, "plan").class, /run-state-running/);
  assert.match(graphNode(elements, "verify").class, /run-state-queued/);
  assert.match(elements["#run-status"].textContent, /^Running 00:00 - Current: Plan/);
  assert.equal(elements["#cancel-run-button"].disabled, false);

  elements["#cancel-run-button"].listeners.click();
  await runPromise;

  const runRequest = requests.find((request) => request.url === "/api/run");
  assert.equal(runRequest.signal.aborted, true);
  assert.match(logText(elements), /cancel requested; closing run stream/);
  assert.match(logText(elements), /run canceled by browser/);
  assert.match(elements["#run-status"].textContent, /^Canceled in 00:00/);
  assert.equal(elements["#cancel-run-button"].disabled, true);
  assert.equal(elements["#run-button"].disabled, false);
}

async function testPreviewChangeCancelsActiveRunStream() {
  const { elements, requests, runPendingTimers } = await bootHarness({
    templates,
    preview: largeWorkflowPreview(),
    runResponseFor: (_body, requestOptions) =>
      cancellableStreamResponse(requestOptions.signal, [
        { type: "workflow_start", message: "workflow started" },
        { type: "node_start", node_id: "plan", message: "starting plan" },
      ]),
  });

  const runPromise = elements["#run-button"].listeners.click();
  await flushAsyncWork();

  elements["#yaml-editor"].value += "\n# edit";
  elements["#yaml-editor"].listeners.input();
  await runPendingTimers();
  await runPromise;

  const runRequest = requests.find((request) => request.url === "/api/run");
  assert.equal(runRequest.signal.aborted, true);
  assert.match(logText(elements), /preview changed; canceling active run stream/);
  assert.equal(elements["#cancel-run-button"].disabled, true);
  assert.equal(elements["#run-button"].disabled, false);
}

async function testPersistencePanelShowsRunDetailAndCleanupPreview() {
  const { elements } = await bootHarness({
    templates: [
      ...templates,
      { id: "project-flow", name: "Project Flow", yaml: "name: project\nnodes: []\n", source: "project", kind: "workflow", valid: true },
    ],
    previewFor,
    runs: [
      {
        run_id: "run-123",
        workflow_name: "Review",
        workflow_id: "review",
        status: "succeeded",
        created_at: "2026-06-12T12:00:00Z",
        started_at: "2026-06-12T12:00:01Z",
        artifacts_dir: ".micromage/runs/run-123",
      },
    ],
    runDetails: {
      "run-123": {
        run: {
          run_id: "run-123",
          workflow_name: "Review",
          workflow_id: "review",
          status: "succeeded",
          artifacts_dir: ".micromage/runs/run-123",
        },
        manifest: {
          artifacts: [{ node_id: "plan", path: "plan.md", size_bytes: 42 }],
        },
      },
    },
    cleanupReport: {
      dry_run: true,
      candidates: [{ run_id: "run-old", status: "failed", artifacts_dir: ".micromage/runs/run-old" }],
      deleted: [],
      failed: [],
    },
  });

  assert.match(elements["#definition-list"].innerHTML, /project workflow - valid/);
  assert.match(elements["#run-history-list"].innerHTML, /Review/);
  assert.match(elements["#run-history-list"].innerHTML, /succeeded/);

  await elements["#run-history-list"].listeners.click({
    target: { closest: () => ({ dataset: { runId: "run-123" } }) },
  });

  assert.match(elements["#run-detail"].innerHTML, /run-123/);
  assert.match(elements["#run-detail"].innerHTML, /plan: plan\.md \(42 bytes\)/);

  await elements["#cleanup-preview-button"].listeners.click();

  assert.match(elements["#cleanup-preview"].innerHTML, /1 cleanup candidate/);
  assert.match(elements["#cleanup-preview"].innerHTML, /run-old/);
}

async function testSaveDefinitionPostsCurrentYamlAndRefreshesLists() {
  const { elements, requests } = await bootHarness({
    templates,
    previewFor,
    definitionKind: "template",
  });

  elements["#definition-id"].value = "browser-template";
  elements["#definition-kind"].value = "template";
  elements["#yaml-editor"].value = "name: Saved\nnodes:\n  - id: plan\n    prompt: Save\n";

  await elements["#save-definition-button"].listeners.click();

  const saveRequest = requests.find((request) => request.url === "/api/definitions");
  assert.equal(saveRequest.body.id, "browser-template");
  assert.equal(saveRequest.body.kind, "template");
  assert.match(saveRequest.body.yaml, /name: Saved/);
  assert.match(logText(elements), /saved template browser-template/);
  assert.match(elements["#definition-list"].innerHTML, /browser-template/);
  assert.equal(elements["#template-select"].value, "browser-template");
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
  await testGraphFitButtonTogglesActualAndFitSizing();
  await testRunStateOverlaysAndStatusContext();
  await testCancelRunAbortsOpenStream();
  await testPreviewChangeCancelsActiveRunStream();
  await testPersistencePanelShowsRunDetailAndCleanupPreview();
  await testSaveDefinitionPostsCurrentYamlAndRefreshesLists();
  await testRunModeSelectionAndRealModeRejection();
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
