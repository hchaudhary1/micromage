(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) {
    module.exports = api;
  }
  root.MicromageTemplateState = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  function hasTemplateDraftEdits(currentYaml, baselineYaml) {
    return currentYaml !== baselineYaml;
  }

  function templateYaml(templates, templateID) {
    const selected = templates.find((template) => template.id === templateID);
    return selected ? selected.yaml : "";
  }

  function resolveTemplateChange(options) {
    const confirmOverwrite = options.confirmOverwrite || (() => false);
    // Template baselines identify when user-authored YAML would be overwritten.
    const dirty = hasTemplateDraftEdits(options.currentYaml, options.baselineYaml);
    if (dirty && !confirmOverwrite()) {
      return {
        accepted: false,
        selectedTemplateID: options.previousTemplateID,
        yaml: options.currentYaml,
        baselineYaml: options.baselineYaml,
      };
    }

    const yaml = templateYaml(options.templates, options.requestedTemplateID);
    return {
      accepted: true,
      selectedTemplateID: options.requestedTemplateID,
      yaml,
      baselineYaml: yaml,
    };
  }

  function issueCounts(preview) {
    const issues = preview?.issues || [];
    return {
      errors: issues.filter((issue) => issue.level === "error").length,
      warnings: issues.filter((issue) => issue.level === "warning").length,
    };
  }

  function issueScope(issue) {
    const field = issue.field ? ` / ${issue.field}` : "";
    if (issue.node_id) {
      return `Node ${issue.node_id}${field}`;
    }
    return `Workflow${field}`;
  }

  function formatIssue(issue) {
    return `${issueScope(issue)}: ${issue.message}`;
  }

  function realRunPreflightMessage(preview) {
    // Preflight summaries give operators a compact risk ledger before irreversible real-run effects.
    const workflow = preview?.workflow || {};
    const nodes = workflowNodesForPreflight(preview);
    const kindCounts = countKinds(nodes.map((node) => node.kind));
    const bashCount = kindCounts.get("bash") || 0;
    const commandCount = kindCounts.get("command") || 0;
    const providerOverrides = providerModelOverrides(workflow, nodes);

    return [
      "Real run preflight",
      "",
      `Workflow: ${workflow.name || "Workflow"}`,
      `Executable node kinds: ${formatKindCounts(kindCounts)}`,
      `Bash/command nodes: ${bashCount + commandCount} (bash ${bashCount}, command ${commandCount})`,
      `Workflow provider/model: ${formatProviderModel(workflow.provider, workflow.model)}`,
      `Node provider/model overrides: ${providerOverrides || "none"}`,
      "Artifacts: real runs create a repo-local .micromage/runs/<run-id> directory; declared outputs are kept inside that run directory.",
      "Risks: may execute shell commands, send prompts to providers, write artifacts, and modify repository files.",
      "",
      "Start this real run?",
    ].join("\n");
  }

  function workflowNodesForPreflight(preview) {
    const workflowNodes = preview?.workflow?.nodes || [];
    if (workflowNodes.length) {
      return workflowNodes.map((node) => ({
        id: node.id || "node",
        kind: executableKind(node),
        provider: node.provider || "",
        model: node.model || "",
      }));
    }

    const graphNodes = preview?.graph?.nodes || [];
    return graphNodes.map((node) => ({
      id: node.id || "node",
      kind: node.type || "unknown",
      provider: node.metadata?.provider || "",
      model: node.metadata?.model || "",
    }));
  }

  function executableKind(node) {
    for (const kind of ["command", "prompt", "bash", "script", "loop", "approval", "cancel"]) {
      if (Object.hasOwn(node, kind)) {
        return kind;
      }
    }
    return "unknown";
  }

  function countKinds(kinds) {
    const counts = new Map();
    for (const kind of kinds) {
      counts.set(kind, (counts.get(kind) || 0) + 1);
    }
    return counts;
  }

  function formatKindCounts(counts) {
    const parts = [];
    for (const [kind, count] of counts.entries()) {
      parts.push(`${kind} ${count}`);
    }
    return parts.join(", ") || "none";
  }

  function formatProviderModel(provider, model) {
    return `${provider || "server default"} / ${model || "server default"}`;
  }

  function providerModelOverrides(workflow, nodes) {
    return nodes
      .filter((node) => node.provider || node.model)
      .map((node) => `${node.id}: ${formatProviderModel(node.provider || workflow.provider, node.model || workflow.model)}`)
      .join(", ");
  }

  async function readRunRejection(response) {
    const contentType = response.headers?.get?.("Content-Type") || "";
    if (contentType.includes("application/json")) {
      const preview = await response.json();
      return {
        preview,
        summary: "run rejected: validation failed",
        issues: preview?.issues || [],
      };
    }

    const detail = await responseDetail(response);
    return {
      preview: null,
      summary: `run rejected: ${detail}`,
      issues: [],
    };
  }

  async function responseDetail(response) {
    const body = typeof response.text === "function" ? (await response.text()).trim() : "";
    if (body) {
      return body;
    }
    const status = response.status ? `HTTP ${response.status}` : "non-OK response";
    return response.statusText ? `${status} ${response.statusText}` : status;
  }

  return {
    formatIssue,
    hasTemplateDraftEdits,
    issueCounts,
    issueScope,
    realRunPreflightMessage,
    readRunRejection,
    resolveTemplateChange,
  };
});
