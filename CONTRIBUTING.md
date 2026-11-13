# Contributing to AgentView

AgentView is an early Go experiment for observing existing coding-agent sessions. The product reads selected sources; it must not launch, resume, interrupt, configure, or take ownership of an observed agent.

Start with the [README](README.md), [adapter contract](docs/adapters.md), and [test strategy](docs/testing.md). For a substantial change, open an issue describing the problem and the evidence before building a new integration or changing the observation model.

## Local checks

```sh
make ci
```

This runs formatting checks, vet, race-instrumented unit and process integration tests, bounded fuzzing, and a build. It needs no model account or native agent installation. Reports are written to ignored `test-results/`. Native smoke tests are separate opt-in commands that use credentials and may incur model usage.

Keep changes focused. Preserve metadata-only defaults, bounded reads, source provenance, and unknown execution state where the source cannot establish it. New adapters need original synthetic fixtures, primary-source format evidence, and tests for failure/recovery and observer independence. Distinguish fixture-tested behavior from actual native compatibility.

## Bug reports and fixtures

Include the operating system, Go/AgentView version, provider version, command, and a minimal synthetic reproduction. Do not attach real agent transcripts, credentials, private source code, account metadata, or raw native test logs. Unknown format examples should be deliberately sanitized and reviewed before sharing.

## Documentation scope

Keep PRDs, competitor evaluations, internal reviews, role profiles, and unreleased planning notes in `docs/internal/`. That directory and existing internal-document paths are ignored by Git. Do not force-add ignored documents. Public documentation should describe usage, implemented architecture, adapter formats, and reproducible tests. Check `git diff --cached --name-only` before committing to confirm only intended public files are staged.

## License

Contributions to this repository are made under its [MIT License](LICENSE). Preserve any required third-party attribution and do not copy code or fixtures whose license is incompatible with the project.
