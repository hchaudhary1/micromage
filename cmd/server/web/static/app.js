const templateSelect = document.querySelector("#template-select");
const yamlEditor = document.querySelector("#yaml-editor");
const previewState = document.querySelector("#preview-state");
const runButton = document.querySelector("#run-button");
const runArguments = document.querySelector("#run-arguments");
const runMode = document.querySelector("#run-mode");
const cancelRunButton = document.querySelector("#cancel-run-button");
const workflowSummary = document.querySelector("#workflow-summary");
const workflowName = document.querySelector("#workflow-name");
const workflowDescription = document.querySelector("#workflow-description");
const runStatus = document.querySelector("#run-status");
const issueCounts = document.querySelector("#issue-counts");
const issuePanel = document.querySelector("#issue-panel");
const graphWrap = document.querySelector(".graph-wrap");
const dagSvg = document.querySelector("#dag-svg");
const fitGraphButton = document.querySelector("#fit-graph-button");
const inspectorBody = document.querySelector("#inspector-body");
const runLog = document.querySelector("#run-log");
const definitionKind = document.querySelector("#definition-kind");
const definitionID = document.querySelector("#definition-id");
const saveDefinitionButton = document.querySelector("#save-definition-button");
const refreshPersistenceButton = document.querySelector("#refresh-persistence-button");
const cleanupPreviewButton = document.querySelector("#cleanup-preview-button");
const definitionList = document.querySelector("#definition-list");
const runHistoryList = document.querySelector("#run-history-list");
const cleanupPreview = document.querySelector("#cleanup-preview");
const runDetail = document.querySelector("#run-detail");
const appState = window.MicromageTemplateState;

const SVG_NS = "http://www.w3.org/2000/svg";
const NODE_WIDTH = 190;
const NODE_HEIGHT = 82;
let currentPreview = null;
let selectedNodeId = "";
let previewTimer = 0;
let currentTemplateID = "";
let templateBaselineYaml = "";
let graphFitToScreen = false;
let nodeRunStates = new Map();
let runStartedAt = 0;
let runStatusTimer = 0;
let currentRunNodeId = "";
let activeRunController = null;
let templates = [];

async function boot() {
  templates = await loadTemplates();
  renderTemplateOptions(templates);
  renderDefinitionList(templates);
  templateSelect.addEventListener("change", () => {
    // Manual workflow edits need an explicit overwrite decision before template swaps.
    const change = appState.resolveTemplateChange({
      templates,
      requestedTemplateID: templateSelect.value,
      previousTemplateID: currentTemplateID,
      currentYaml: yamlEditor.value,
      baselineYaml: templateBaselineYaml,
      confirmOverwrite: () => window.confirm("Switch templates and discard your YAML edits?"),
    });
    if (!change.accepted) {
      templateSelect.value = change.selectedTemplateID;
      return;
    }

    currentTemplateID = change.selectedTemplateID;
    templateBaselineYaml = change.baselineYaml;
    yamlEditor.value = change.yaml;
    definitionID.value = currentTemplateID;
    selectedNodeId = "";
    updatePreview();
  });
  yamlEditor.addEventListener("input", () => {
    window.clearTimeout(previewTimer);
    previewTimer = window.setTimeout(updatePreview, 220);
  });
  runButton.addEventListener("click", runWorkflow);
  cancelRunButton.addEventListener("click", cancelRun);
  fitGraphButton.addEventListener("click", toggleGraphFit);
  saveDefinitionButton.addEventListener("click", saveDefinition);
  refreshPersistenceButton.addEventListener("click", refreshPersistence);
  cleanupPreviewButton.addEventListener("click", previewRunCleanup);
  definitionList.addEventListener("click", loadDefinitionFromList);
  runHistoryList.addEventListener("click", loadRunDetailFromList);
  runMode.addEventListener("change", updatePreview);
  issuePanel.addEventListener("click", (event) => {
    const target = event.target.closest?.("[data-node-id]");
    if (!target) return;
    selectedNodeId = target.dataset.nodeId;
    renderGraph(normalizedGraph(currentPreview?.graph));
    renderInspector();
  });

  const first = templates[0];
  currentTemplateID = first ? first.id : "";
  templateBaselineYaml = first ? first.yaml : "";
  yamlEditor.value = templateBaselineYaml;
  templateSelect.value = currentTemplateID;
  definitionID.value = currentTemplateID;
  await refreshPersistence();
  await updatePreview();
}

async function loadTemplates() {
  const response = await fetch("/api/templates");
  return response.json();
}

function renderTemplateOptions(items) {
  templateSelect.innerHTML = items
    .map((template) => `<option value="${escapeHTML(template.id)}">${escapeHTML(template.name || template.id)}</option>`)
    .join("");
}

async function updatePreview() {
  cancelActiveRunForPreviewChange();
  setState("Previewing", "busy");
  const response = await fetch("/api/preview", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      yaml: yamlEditor.value,
      arguments: runArguments.value,
      mode: runMode.value,
    }),
  });
  currentPreview = await response.json();
  clearRunState();
  renderPreview(currentPreview);
}

function renderPreview(preview) {
  const counts = appState.issueCounts(preview);
  const graph = normalizedGraph(preview.graph);
  workflowSummary.textContent = `${graph.nodes.length} nodes`;
  workflowName.textContent = preview.workflow?.name || "Workflow";
  workflowDescription.textContent = preview.workflow?.description || "";
  issueCounts.innerHTML = [
    `<span class="issue-badge error">${counts.errors} errors</span>`,
    `<span class="issue-badge warning">${counts.warnings} warnings</span>`,
  ].join("");
  renderIssuePanel(preview);
  runButton.disabled = !preview.can_run;
  setState(preview.can_run ? "Runnable" : "Invalid", preview.can_run ? "ok" : "error");
  renderGraph(graph);
  renderInspector();
}

async function refreshPersistence() {
  templates = await loadTemplates();
  renderTemplateOptions(templates);
  if (currentTemplateID) {
    templateSelect.value = currentTemplateID;
  }
  renderDefinitionList(templates);
  await refreshRunHistory();
}

async function refreshRunHistory() {
  const response = await fetch("/api/runs");
  if (!response.ok) {
    runHistoryList.textContent = "Run history unavailable";
    return;
  }
  const runs = await response.json();
  renderRunHistory(runs || []);
}

function renderDefinitionList(items) {
  if (!items.length) {
    definitionList.innerHTML = `<div class="persistence-empty">No saved definitions</div>`;
    return;
  }
  definitionList.innerHTML = items.map(renderDefinitionRow).join("");
}

function renderDefinitionRow(template) {
  const source = template.source || "embedded";
  const kind = template.kind || "workflow";
  const label = template.name || template.id;
  const status = template.validation_status || (template.valid ? "valid" : "invalid");
  return [
    `<button type="button" class="persistence-row" data-definition-id="${escapeHTML(template.id)}">`,
    `<span>${escapeHTML(label)}</span>`,
    `<small>${escapeHTML(source)} ${escapeHTML(kind)} - ${escapeHTML(status)}</small>`,
    `</button>`,
  ].join("");
}

function loadDefinitionFromList(event) {
  const target = event.target.closest?.("[data-definition-id]");
  if (!target) return;
  const template = templates.find((item) => item.id === target.dataset.definitionId);
  if (!template) return;
  // Saved definitions load through the same dirty-check path as starter templates.
  const change = appState.resolveTemplateChange({
    templates,
    requestedTemplateID: template.id,
    previousTemplateID: currentTemplateID,
    currentYaml: yamlEditor.value,
    baselineYaml: templateBaselineYaml,
    confirmOverwrite: () => window.confirm("Load this saved definition and discard your YAML edits?"),
  });
  if (!change.accepted) return;
  currentTemplateID = change.selectedTemplateID;
  templateBaselineYaml = change.baselineYaml;
  yamlEditor.value = change.yaml;
  templateSelect.value = currentTemplateID;
  definitionID.value = currentTemplateID;
  selectedNodeId = "";
  updatePreview();
}

async function saveDefinition() {
  const id = (definitionID.value || currentTemplateID || slugID(currentPreview?.workflow?.name) || "workflow").trim();
  const kind = definitionKind.value || "workflow";
  const response = await fetch("/api/definitions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ id, kind, yaml: yamlEditor.value }),
  });
  if (!response.ok) {
    appendLog(`save failed: ${(await response.text()).trim() || response.statusText}`, "log-error");
    return;
  }
  const saved = await response.json();
  // Browser saves keep edited workflows reusable without shell access to .micromage.
  currentTemplateID = saved.id;
  templateBaselineYaml = saved.yaml;
  definitionID.value = saved.id;
  appendLog(`saved ${saved.kind || kind} ${saved.id}`, "log-detail");
  await refreshPersistence();
  templateSelect.value = currentTemplateID;
}

function renderRunHistory(runs) {
  if (!runs.length) {
    runHistoryList.innerHTML = `<div class="persistence-empty">No runs yet</div>`;
    return;
  }
  runHistoryList.innerHTML = runs.map(renderRunRow).join("");
}

function renderRunRow(run) {
  const name = run.workflow_name || run.workflow_id || run.run_id;
  const status = run.status || "unknown";
  const started = formatTimestamp(run.started_at || run.created_at);
  return [
    `<button type="button" class="persistence-row status-${escapeHTML(status)}" data-run-id="${escapeHTML(run.run_id)}">`,
    `<span>${escapeHTML(name)}</span>`,
    `<small>${escapeHTML(status)} - ${escapeHTML(started)}</small>`,
    `</button>`,
  ].join("");
}

async function loadRunDetailFromList(event) {
  const target = event.target.closest?.("[data-run-id]");
  if (!target) return;
  await showRunDetail(target.dataset.runId);
}

async function showRunDetail(runID) {
  const response = await fetch(`/api/runs/${encodeURIComponent(runID)}`);
  if (!response.ok) {
    runDetail.textContent = "Run detail unavailable";
    runDetail.classList.add("muted");
    return;
  }
  renderRunDetail(await response.json());
}

function renderRunDetail(detail) {
  const run = detail.run || {};
  const manifest = detail.manifest;
  const summary = detail.summary;
  const artifacts = manifest?.artifacts || summary?.artifacts || [];
  const artifactRows = artifacts.length
    ? artifacts.map((artifact) => `<li>${escapeHTML(artifact.node_id || artifact.nodeID || "artifact")}: ${escapeHTML(artifact.path || "")}${artifact.size_bytes ? ` (${artifact.size_bytes} bytes)` : ""}</li>`).join("")
    : "<li>No artifacts recorded</li>";
  runDetail.classList.remove("muted");
  runDetail.innerHTML = [
    `<strong>${escapeHTML(run.run_id || "Run")}</strong>`,
    `<dl>`,
    `<dt>Status</dt><dd>${escapeHTML(run.status || "unknown")}</dd>`,
    `<dt>Workflow</dt><dd>${escapeHTML(run.workflow_name || run.workflow_id || "unknown")}</dd>`,
    `<dt>Artifacts</dt><dd>${escapeHTML(run.artifacts_dir || "not recorded")}</dd>`,
    `</dl>`,
    `<ul>${artifactRows}</ul>`,
  ].join("");
}

async function previewRunCleanup() {
  const response = await fetch("/api/runs/cleanup/preview", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({}),
  });
  const report = await response.json();
  renderCleanupPreview(report);
}

function renderCleanupPreview(report) {
  const candidates = report.candidates || [];
  cleanupPreview.classList.remove("muted");
  cleanupPreview.innerHTML = candidates.length
    ? `<strong>${candidates.length} cleanup candidate${candidates.length === 1 ? "" : "s"}</strong><ul>${candidates.map((run) => `<li>${escapeHTML(run.run_id)} - ${escapeHTML(run.status)} - ${escapeHTML(run.artifacts_dir || "")}</li>`).join("")}</ul>`
    : "<strong>No cleanup candidates</strong>";
}

function normalizedGraph(graph) {
  // Invalid YAML can still carry actionable issues even when graph collections are absent.
  return {
    edges: graph?.edges || [],
    height: graph?.height || 0,
    nodes: graph?.nodes || [],
    width: graph?.width || 0,
  };
}

function renderIssuePanel(preview) {
  const issues = preview?.issues || [];
  if (!issues.length) {
    issuePanel.classList.add("empty");
    issuePanel.innerHTML = "";
    issuePanel.replaceChildren();
    return;
  }

  issuePanel.classList.remove("empty");
  // Global issues make validation blockers fixable without selecting each graph node.
  issuePanel.innerHTML = [
    `<h3>${issues.length} issue${issues.length === 1 ? "" : "s"}</h3>`,
    `<ul>${issues.map(renderIssueItem).join("")}</ul>`,
  ].join("");
}

function renderIssueItem(issue) {
  const scope = escapeHTML(appState.issueScope(issue));
  const level = escapeHTML(issue.level || "warning");
  const message = escapeHTML(issue.message || "");
  const scopeHTML = issue.node_id
    ? `<button type="button" class="issue-link" data-node-id="${escapeHTML(issue.node_id)}">${scope}</button>`
    : `<span class="issue-scope">${scope}</span>`;
  return `<li class="${level}">${scopeHTML}<span class="issue-message">${message}</span></li>`;
}

function renderGraph(graph) {
  dagSvg.replaceChildren();
  const graphWidth = Math.max(graph.width, 720);
  const graphHeight = Math.max(graph.height, 420);
  dagSvg.setAttribute("viewBox", `0 0 ${graphWidth} ${graphHeight}`);
  dagSvg.setAttribute("width", graphFitToScreen ? "100%" : String(graphWidth));
  dagSvg.setAttribute("height", graphFitToScreen ? "100%" : String(graphHeight));
  dagSvg.setAttribute("data-fit", graphFitToScreen ? "fit" : "actual");
  graphWrap.className = `graph-wrap${graphFitToScreen ? " fit-to-screen" : ""}`;
  fitGraphButton.textContent = graphFitToScreen ? "Actual size" : "Fit";

  const nodeById = new Map(graph.nodes.map((node) => [node.id, node]));
  for (const edge of graph.edges) {
    const source = nodeById.get(edge.source);
    const target = nodeById.get(edge.target);
    if (!source || !target) continue;
    const path = svg("path", {
      class: "edge",
      d: edgePath(source, target),
      "marker-end": "url(#arrow)",
    });
    dagSvg.appendChild(path);
  }

  const defs = svg("defs");
  const marker = svg("marker", {
    id: "arrow",
    markerWidth: "10",
    markerHeight: "10",
    refX: "8",
    refY: "3",
    orient: "auto",
  });
  marker.appendChild(svg("path", { d: "M0,0 L0,6 L9,3 z", class: "arrow" }));
  defs.appendChild(marker);
  dagSvg.prepend(defs);

  for (const node of graph.nodes) {
    dagSvg.appendChild(renderNode(node));
  }
}

function renderNode(node) {
  const runState = nodeRunStates.get(node.id) || "";
  const group = svg("g", {
    class: `dag-node type-${node.type}${selectedNodeId === node.id ? " selected" : ""}${(node.issues || []).length ? " has-issues" : ""}${runState ? ` run-state-${runState}` : ""}`,
    transform: `translate(${node.x}, ${node.y})`,
    tabindex: "0",
    "aria-label": runState ? `${node.id} ${runState}` : node.id,
  });
  group.addEventListener("click", () => {
    selectedNodeId = node.id;
    renderGraph(normalizedGraph(currentPreview?.graph));
    renderInspector();
  });
  group.appendChild(svg("rect", { width: NODE_WIDTH, height: NODE_HEIGHT, rx: "8" }));
  group.appendChild(svg("rect", { class: "node-stripe", width: "5", height: NODE_HEIGHT, rx: "3" }));
  group.appendChild(textEl(node.type.toUpperCase(), 14, 20, "node-type"));
  group.appendChild(textEl(node.label || node.id, 14, 42, "node-label"));
  group.appendChild(textEl(node.summary || node.id, 14, 62, "node-summary"));
  const badges = node.badges || [];
  if (badges.length) {
    group.appendChild(textEl(badges.slice(0, 2).join(" / "), 118, 20, "node-badge"));
  }
  if (runState) {
    // Run-state overlays make long workflows auditable while the stream is still active.
    group.appendChild(textEl(runState.toUpperCase(), 118, 66, `node-run-state node-run-state-${runState}`));
  }
  return group;
}

function renderInspector() {
  const node = normalizedGraph(currentPreview?.graph).nodes.find((item) => item.id === selectedNodeId);
  if (!node) {
    inspectorBody.classList.add("muted");
    inspectorBody.textContent = "Select a node";
    return;
  }
  inspectorBody.classList.remove("muted");
  const metadata = Object.entries(node.metadata || {})
    .map(([key, value]) => `<dt>${escapeHTML(key)}</dt><dd>${escapeHTML(value)}</dd>`)
    .join("");
  const issues = (node.issues || [])
    .map((issue) => `<li class="${escapeHTML(issue.level)}">${escapeHTML(issue.message)}</li>`)
    .join("");
  const runState = nodeRunStates.get(node.id);
  const runMetadata = runState ? `<dt>Run state</dt><dd>${escapeHTML(runState)}</dd>` : "";
  inspectorBody.innerHTML = `<dl>${runMetadata}${metadata}</dl>${issues ? `<ul>${issues}</ul>` : ""}`;
}

async function runWorkflow() {
  if (runMode.value === "real" && !confirmRealRunPreflight()) {
    appendLog("real run canceled");
    runButton.disabled = !currentPreview?.can_run;
    return;
  }
  runButton.disabled = true;
  cancelRunButton.disabled = false;
  runLog.textContent = "";
  appendLog("opening run stream");
  startRunState();
  const controller = new AbortController();
  activeRunController = controller;
  let runFinished = false;
  try {
    const response = await fetch("/api/run", {
      method: "POST",
      headers: runRequestHeaders(),
      signal: controller.signal,
      body: JSON.stringify({
        yaml: yamlEditor.value,
        arguments: runArguments.value,
        mode: runMode.value,
      }),
    });
    if (!response.ok) {
      const rejection = await appState.readRunRejection(response);
      clearRunState();
      if (rejection.preview) {
        // Rejected real-run previews keep actionable validation visible after Run.
        currentPreview = rejection.preview;
        renderPreview(currentPreview);
      }
      appendLog(rejection.summary, "log-error");
      appendPreviewIssues(rejection.issues);
      return;
    }

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      const chunks = buffer.split("\n\n");
      buffer = chunks.pop();
      for (const chunk of chunks) {
        const dataLine = chunk.split("\n").find((line) => line.startsWith("data: "));
        if (!dataLine) continue;
        const event = JSON.parse(dataLine.slice(6));
        appendRunEvent(event);
      }
    }
    runFinished = true;
    if (runStatus.textContent.startsWith("Running ")) {
      finishRunStatus("Finished");
    }
  } catch (error) {
    if (!isAbortError(error)) {
      finishRunStatus("Failed");
      throw error;
    }
    finishRunStatus("Canceled");
    appendLog("run canceled by browser; server stops when the stream connection closes", "log-detail");
  } finally {
    activeRunController = null;
    cancelRunButton.disabled = true;
    stopRunStatusTimer();
    if (!runFinished && !currentRunNodeId && !runStartedAt) {
      runStatus.textContent = "No run active";
    }
    runButton.disabled = !currentPreview?.can_run;
    refreshRunHistory().catch((error) => appendLog(error.message, "log-error"));
  }
}

function confirmRealRunPreflight() {
  // Real-run confirmation makes local execution and artifact side effects explicit before they start.
  return window.confirm(appState.realRunPreflightMessage(currentPreview));
}

function runRequestHeaders() {
  const headers = { "Content-Type": "application/json" };
  const token = window.localStorage?.getItem("micromageRealRunToken") || "";
  if (token) {
    // Real-run tokens keep browser-triggered shell execution operator-approved.
    headers.Authorization = `Bearer ${token}`;
  }
  return headers;
}

function appendPreviewIssues(issues) {
  for (const issue of issues || []) {
    appendLog(appState.formatIssue(issue), issue.level === "error" ? "log-error" : "log-detail");
  }
}

function appendRunEvent(event) {
  appendLog(event.message || event.type, event.type === "node_failed" || event.type === "workflow_failed" ? "log-error" : "");
  applyRunEvent(event);
  if (event.type === "run_summary") {
    appendRunSummary(event);
  }
}

function appendRunSummary(event) {
  appendLog(`Run ID: ${event.run_id || "unknown"}`, "log-detail");
  appendLog(`Run directory: ${event.artifacts_dir || "not available"}`, "log-detail");
  appendLog(`Completed nodes: ${(event.completed_nodes || []).join(", ") || "none"}`, "log-detail");
  const failures = (event.failed_nodes || []).map((failure) => `${failure.node_id}: ${failure.message}`);
  appendLog(`Failed nodes: ${failures.join(" | ") || "none"}`, failures.length ? "log-error" : "log-detail");
  for (const artifact of event.artifacts || []) {
    appendLog(`Artifact ${artifact.node_id}: ${artifact.path}`, "log-detail");
  }
}

function appendLog(message, className = "") {
  const line = document.createElement("div");
  line.textContent = message;
  if (className) {
    line.className = className;
  }
  runLog.appendChild(line);
  runLog.scrollTop = runLog.scrollHeight;
}

function setState(text, mode) {
  previewState.textContent = text;
  previewState.className = `state-pill ${mode}`;
}

function toggleGraphFit() {
  graphFitToScreen = !graphFitToScreen;
  renderGraph(normalizedGraph(currentPreview?.graph));
}

function cancelRun() {
  if (!activeRunController) return;
  // Client abort is the safest cancellation path because the server already scopes work to request context.
  appendLog("cancel requested; closing run stream", "log-detail");
  activeRunController.abort();
}

function cancelActiveRunForPreviewChange() {
  if (!activeRunController) return;
  // Workflow edits cancel the visible stream so operators do not monitor stale run state.
  appendLog("preview changed; canceling active run stream", "log-detail");
  activeRunController.abort();
}

function startRunState() {
  runStartedAt = Date.now();
  currentRunNodeId = "";
  nodeRunStates = new Map(normalizedGraph(currentPreview?.graph).nodes.map((node) => [node.id, "queued"]));
  renderGraph(normalizedGraph(currentPreview?.graph));
  updateRunStatusText();
  stopRunStatusTimer();
  runStatusTimer = window.setInterval(updateRunStatusText, 1000);
}

function clearRunState() {
  nodeRunStates = new Map();
  runStartedAt = 0;
  currentRunNodeId = "";
  activeRunController = null;
  cancelRunButton.disabled = true;
  stopRunStatusTimer();
  runStatus.textContent = "No run active";
}

function applyRunEvent(event) {
  if (event.node_id) {
    if (event.type === "node_start") {
      currentRunNodeId = event.node_id;
      nodeRunStates.set(event.node_id, "running");
    } else if (event.type === "node_complete") {
      nodeRunStates.set(event.node_id, "succeeded");
      if (currentRunNodeId === event.node_id) currentRunNodeId = "";
    } else if (event.type === "node_failed") {
      nodeRunStates.set(event.node_id, "failed");
      if (currentRunNodeId === event.node_id) currentRunNodeId = "";
    } else if (event.type === "node_skipped") {
      nodeRunStates.set(event.node_id, "skipped");
      if (currentRunNodeId === event.node_id) currentRunNodeId = "";
    }
  }

  if (event.type === "run_summary") {
    for (const nodeID of event.completed_nodes || []) {
      nodeRunStates.set(nodeID, "succeeded");
    }
    for (const failure of event.failed_nodes || []) {
      if (failure.node_id) nodeRunStates.set(failure.node_id, "failed");
    }
    currentRunNodeId = "";
  }

  if (event.type === "workflow_complete") {
    finishRunStatus("Succeeded");
  } else if (event.type === "workflow_failed") {
    if (currentRunNodeId) nodeRunStates.set(currentRunNodeId, "failed");
    finishRunStatus("Failed");
  } else if (event.type === "workflow_interrupted") {
    finishRunStatus("Interrupted");
  } else {
    updateRunStatusText();
  }
  renderGraph(normalizedGraph(currentPreview?.graph));
  renderInspector();
}

function finishRunStatus(label) {
  stopRunStatusTimer();
  const elapsed = formatElapsed(Date.now() - runStartedAt);
  runStatus.textContent = `${label} in ${elapsed}`;
  currentRunNodeId = "";
}

function updateRunStatusText() {
  if (!runStartedAt) {
    runStatus.textContent = "No run active";
    return;
  }
  const elapsed = formatElapsed(Date.now() - runStartedAt);
  const current = currentRunNodeId ? `Current: ${nodeDisplayName(currentRunNodeId)}` : "Current: waiting";
  runStatus.textContent = `Running ${elapsed} - ${current}`;
}

function stopRunStatusTimer() {
  if (!runStatusTimer) return;
  window.clearInterval(runStatusTimer);
  runStatusTimer = 0;
}

function nodeDisplayName(nodeID) {
  const node = normalizedGraph(currentPreview?.graph).nodes.find((item) => item.id === nodeID);
  return node?.label || nodeID;
}

function formatElapsed(milliseconds) {
  const totalSeconds = Math.max(0, Math.floor(milliseconds / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  const mmss = `${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
  return hours ? `${hours}:${mmss}` : mmss;
}

function isAbortError(error) {
  return error?.name === "AbortError";
}

function formatTimestamp(value) {
  if (!value) return "not started";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return date.toLocaleString();
}

function slugID(value) {
  return String(value || "")
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

function edgePath(source, target) {
  const sx = source.x + NODE_WIDTH;
  const sy = source.y + NODE_HEIGHT / 2;
  const tx = target.x;
  const ty = target.y + NODE_HEIGHT / 2;
  const mid = (tx - sx) / 2;
  return `M ${sx} ${sy} C ${sx + mid} ${sy}, ${tx - mid} ${ty}, ${tx} ${ty}`;
}

function svg(tag, attrs = {}) {
  const element = document.createElementNS(SVG_NS, tag);
  for (const [key, value] of Object.entries(attrs)) {
    element.setAttribute(key, value);
  }
  return element;
}

function textEl(value, x, y, className) {
  const text = svg("text", { x, y, class: className });
  text.textContent = value;
  return text;
}

function escapeHTML(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}

boot().catch((error) => {
  setState("Failed", "error");
  appendLog(error.message);
});
