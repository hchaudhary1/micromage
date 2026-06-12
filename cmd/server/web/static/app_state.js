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
    readRunRejection,
    resolveTemplateChange,
  };
});
