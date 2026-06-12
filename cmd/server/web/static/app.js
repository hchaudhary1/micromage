const templateSelect = document.querySelector("#template-select");
const yamlEditor = document.querySelector("#yaml-editor");
const previewState = document.querySelector("#preview-state");
const runButton = document.querySelector("#run-button");
const runArguments = document.querySelector("#run-arguments");
const runMode = document.querySelector("#run-mode");
const workflowSummary = document.querySelector("#workflow-summary");
const workflowName = document.querySelector("#workflow-name");
const workflowDescription = document.querySelector("#workflow-description");
const issueCounts = document.querySelector("#issue-counts");
const issuePanel = document.querySelector("#issue-panel");
const dagSvg = document.querySelector("#dag-svg");
const inspectorBody = document.querySelector("#inspector-body");
const runLog = document.querySelector("#run-log");
const appState = window.MicromageTemplateState;

const SVG_NS = "http://www.w3.org/2000/svg";
const NODE_WIDTH = 190;
const NODE_HEIGHT = 82;
let currentPreview = null;
let selectedNodeId = "";
let previewTimer = 0;
let currentTemplateID = "";
let templateBaselineYaml = "";

async function boot() {
  const response = await fetch("/api/templates");
  const templates = await response.json();
  templateSelect.innerHTML = templates
    .map((template) => `<option value="${escapeHTML(template.id)}">${escapeHTML(template.name || template.id)}</option>`)
    .join("");
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
    selectedNodeId = "";
    updatePreview();
  });
  yamlEditor.addEventListener("input", () => {
    window.clearTimeout(previewTimer);
    previewTimer = window.setTimeout(updatePreview, 220);
  });
  runButton.addEventListener("click", runWorkflow);
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
  await updatePreview();
}

async function updatePreview() {
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
  dagSvg.setAttribute("viewBox", `0 0 ${Math.max(graph.width, 720)} ${Math.max(graph.height, 420)}`);
  dagSvg.setAttribute("width", "100%");
  dagSvg.setAttribute("height", "100%");

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
  const group = svg("g", {
    class: `dag-node type-${node.type}${selectedNodeId === node.id ? " selected" : ""}${(node.issues || []).length ? " has-issues" : ""}`,
    transform: `translate(${node.x}, ${node.y})`,
    tabindex: "0",
  });
  group.addEventListener("click", () => {
    selectedNodeId = node.id;
    renderGraph(currentPreview.graph);
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
  inspectorBody.innerHTML = `<dl>${metadata}</dl>${issues ? `<ul>${issues}</ul>` : ""}`;
}

async function runWorkflow() {
  if (runMode.value === "real" && !confirmRealRunPreflight()) {
    appendLog("real run canceled");
    runButton.disabled = !currentPreview?.can_run;
    return;
  }
  runButton.disabled = true;
  runLog.textContent = "";
  appendLog("opening run stream");
  const response = await fetch("/api/run", {
    method: "POST",
    headers: runRequestHeaders(),
    body: JSON.stringify({
      yaml: yamlEditor.value,
      arguments: runArguments.value,
      mode: runMode.value,
    }),
  });
  if (!response.ok) {
    const rejection = await appState.readRunRejection(response);
    if (rejection.preview) {
      // Rejected real-run previews keep actionable validation visible after Run.
      currentPreview = rejection.preview;
      renderPreview(currentPreview);
    }
    appendLog(rejection.summary, "log-error");
    appendPreviewIssues(rejection.issues);
    runButton.disabled = !currentPreview?.can_run;
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
  runButton.disabled = !currentPreview?.can_run;
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
  appendLog(event.message, event.type === "node_failed" || event.type === "workflow_failed" ? "log-error" : "");
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
