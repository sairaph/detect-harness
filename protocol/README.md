# detect-harness protocol v1

The companion binary reads exactly one JSON request from standard input and
writes exactly one JSON response to standard output. Configuration, including
environment values, must never be passed in process arguments.

Operations:

- `detect`: return all harness detection results.
- `render`: render a standalone client configuration without writing it.
- `update`: plan selected harness changes and optionally apply them. Set
  `dryRun` to return only `changes`.

The process exits `0` for a valid protocol operation, even when individual
harness results are conflicts or failures. It exits non-zero only when the
request or operation itself is invalid. Consumers must inspect both `ok` and
each per-harness result.
