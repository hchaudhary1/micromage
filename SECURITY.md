# Security Policy

## Supported Versions

Micromage is maintained on the `main` branch. Security fixes are expected to land there first until the project publishes versioned release branches.

## Reporting A Vulnerability

Do not open a public issue for an unpatched security vulnerability.

Prefer GitHub private vulnerability reporting for this repository when it is available. If private reporting is not available, contact the maintainer privately before public disclosure.

Include:

- A clear description of the issue
- Impact and affected area
- Reproduction steps or proof of concept
- Any proposed mitigation

## Scope

This policy covers the Micromage Go server, embedded browser UI, workflow parsing and execution code, bundled workflow templates, release artifacts built from this repository, and local `.micromage/` run artifacts created by the server.

Out of scope:

- Vulnerabilities in third-party local tools invoked by workflows, such as the OpenCode CLI
- Unsafe workflows or shell commands intentionally supplied by an operator
- Exposed deployments that bypass the documented trusted-environment model

## Operator Guidance

Micromage can execute real workflow nodes that read and modify the local working tree. Treat the server as a local operator tool, not as a public multi-tenant service.

- Keep the default `127.0.0.1` bind address unless a trusted reverse proxy or private network boundary is in place.
- Enable real runs only with `MICROMAGE_ENABLE_REAL_RUNS=1` and a high-entropy `MICROMAGE_REAL_RUN_TOKEN`.
- Keep `MICROMAGE_OPENCODE_UNSAFE` unset unless the workflow is intentionally allowed to bypass OpenCode permission prompts.
- Do not commit `.env`, `.micromage/`, generated artifacts, provider tokens, or workflow outputs that may contain secrets.
- Review [docs/operator-security.md](docs/operator-security.md) before running real workflows on production repositories.
