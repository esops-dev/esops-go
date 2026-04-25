<!--
Thanks for the contribution. Fill in the sections below; remove any
that don't apply. Keep the description focused on what changed and
why — the diff already shows how.
-->

## Summary

<!-- One or two sentences. What problem does this solve? -->

## Linked issue

<!-- e.g. Closes #42. Open an issue first for non-trivial changes. -->

## Type of change

- [ ] Bug fix
- [ ] New feature / command / flag
- [ ] Refactor (no user-visible change)
- [ ] Documentation
- [ ] Build / CI / release plumbing
- [ ] Dependency change

## Test plan

<!--
How did you verify this works? Tick what applies and add specifics.
For UI-less CLI work, real cluster verification is what counts.
-->

- [ ] `make lint test security` is green locally
- [ ] Added or updated unit tests

## User-visible changes

<!-- Flags, commands, output formats, exit codes, config keys. -->

## Documentation

- [ ] Updated [README.md](../README.md) (global flags / exit codes / commands)
- [ ] Updated [docs/](../docs/) for the affected command
- [ ] Updated [config/esops.example.yaml](../config/esops.example.yaml)
- [ ] No docs change needed (please explain why)

## Destructive operation checklist

<!-- Skip this section if the change is read-only. -->

- [ ] Supports `--dry-run` and exits `7` on a dry-run blocker
- [ ] Computes a blast radius and refuses past the threshold without
      an explicit opt-in flag
- [ ] Honours per-context protection tier (`prod` requires
      `--i-know-this-is-prod`, exit `9` otherwise)
- [ ] Uses a purpose-built confirmation flag, not a generic `--force`

## Dependency changes

<!--
If this PR adds, removes, or upgrades a dependency, justify it:
- What problem does it solve that stdlib + existing deps can't?
- Direct deps are capped at ≈12. What's the new total?
- Any transitive bloat? Run `go mod why` for context.
-->

## Backwards compatibility

<!--
Are exit codes, flag names, output schemas, or config keys changing?
Pre-1.0 we're allowed to break things, but say so explicitly so
release notes capture it.
-->

## Anything else

<!-- Screenshots, before/after output, design notes, follow-ups. -->
