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

  return {
    hasTemplateDraftEdits,
    resolveTemplateChange,
  };
});
