const assert = require("node:assert/strict");
const { resolveTemplateChange } = require("../../../cmd/server/web/static/app_state.js");

const templates = [
  { id: "starter", yaml: "name: starter\nnodes: []\n" },
  { id: "review", yaml: "name: review\nnodes: []\n" },
];

function testNoEditSwitchDoesNotPrompt() {
  let prompts = 0;
  const change = resolveTemplateChange({
    templates,
    requestedTemplateID: "review",
    previousTemplateID: "starter",
    currentYaml: templates[0].yaml,
    baselineYaml: templates[0].yaml,
    confirmOverwrite: () => {
      prompts += 1;
      return true;
    },
  });

  assert.equal(prompts, 0);
  assert.equal(change.accepted, true);
  assert.equal(change.selectedTemplateID, "review");
  assert.equal(change.yaml, templates[1].yaml);
  assert.equal(change.baselineYaml, templates[1].yaml);
}

function testEditedYamlCancelKeepsDraftAndPreviousTemplate() {
  let prompts = 0;
  const editedYaml = templates[0].yaml + "# manual edit\n";
  const change = resolveTemplateChange({
    templates,
    requestedTemplateID: "review",
    previousTemplateID: "starter",
    currentYaml: editedYaml,
    baselineYaml: templates[0].yaml,
    confirmOverwrite: () => {
      prompts += 1;
      return false;
    },
  });

  assert.equal(prompts, 1);
  assert.equal(change.accepted, false);
  assert.equal(change.selectedTemplateID, "starter");
  assert.equal(change.yaml, editedYaml);
  assert.equal(change.baselineYaml, templates[0].yaml);
}

function testEditedYamlConfirmSwitchesAndResetsBaseline() {
  let prompts = 0;
  const change = resolveTemplateChange({
    templates,
    requestedTemplateID: "review",
    previousTemplateID: "starter",
    currentYaml: templates[0].yaml + "# manual edit\n",
    baselineYaml: templates[0].yaml,
    confirmOverwrite: () => {
      prompts += 1;
      return true;
    },
  });

  assert.equal(prompts, 1);
  assert.equal(change.accepted, true);
  assert.equal(change.selectedTemplateID, "review");
  assert.equal(change.yaml, templates[1].yaml);
  assert.equal(change.baselineYaml, templates[1].yaml);
}

function testMissingTemplateFallsBackToEmptyYaml() {
  const change = resolveTemplateChange({
    templates,
    requestedTemplateID: "missing",
    previousTemplateID: "starter",
    currentYaml: templates[0].yaml,
    baselineYaml: templates[0].yaml,
    confirmOverwrite: () => {
      throw new Error("no prompt expected for unchanged YAML");
    },
  });

  assert.equal(change.accepted, true);
  assert.equal(change.selectedTemplateID, "missing");
  assert.equal(change.yaml, "");
  assert.equal(change.baselineYaml, "");
}

testNoEditSwitchDoesNotPrompt();
testEditedYamlCancelKeepsDraftAndPreviousTemplate();
testEditedYamlConfirmSwitchesAndResetsBaseline();
testMissingTemplateFallsBackToEmptyYaml();
