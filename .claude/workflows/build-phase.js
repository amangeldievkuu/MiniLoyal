export const meta = {
  name: 'build-phase',
  description:
    'Build one MiniLoyal phase: module agents in worktrees, then tests, security review and integration',
  whenToUse:
    'Run with args { phase, modules: [{ key, agent, prompt }] } when a phase has several independent modules to build in parallel.',
  phases: [
    { title: 'Build', detail: 'one agent per module, each in its own worktree' },
    { title: 'Test', detail: 'test-engineer hardens the merged set of branches' },
    { title: 'Review', detail: 'security-reviewer reads every branch, no edits' },
    { title: 'Integrate', detail: 'integrator merges to main after make ci' },
  ],
};

// Usage (from the main session, after "use a workflow"):
//   Workflow({ name: 'build-phase', args: {
//     phase: 'Phase 2: LINE identity',
//     modules: [
//       { key: 'lineauth', agent: 'line-integration', prompt: '...' },
//       { key: 'sdk', agent: 'sdk-ts', prompt: '...' },
//     ],
//   }})
// Total agents per run: modules.length + 3. Keep modules at six or fewer.

const MAX_MODULES = 6;

const MODULE_SCHEMA = {
  type: 'object',
  properties: {
    key: { type: 'string' },
    branch: { type: 'string', description: 'git branch the work was committed on' },
    worktree: { type: 'string', description: 'absolute path of the worktree' },
    files_changed: { type: 'array', items: { type: 'string' } },
    tests: {
      type: 'object',
      properties: {
        command: { type: 'string' },
        passed: { type: 'boolean' },
        summary: { type: 'string' },
      },
      required: ['command', 'passed', 'summary'],
    },
    needs: {
      type: 'array',
      description: 'interfaces, migrations or spec changes this module needs from other agents',
      items: { type: 'string' },
    },
    blocked: { type: 'boolean' },
    notes: { type: 'string' },
  },
  required: ['key', 'branch', 'files_changed', 'tests', 'blocked'],
};

const TEST_SCHEMA = {
  type: 'object',
  properties: {
    branch: { type: 'string' },
    invariants_covered: { type: 'array', items: { type: 'string' } },
    bugs_found: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          module: { type: 'string' },
          summary: { type: 'string' },
          repro: { type: 'string' },
        },
        required: ['module', 'summary'],
      },
    },
    passed: { type: 'boolean' },
    summary: { type: 'string' },
  },
  required: ['passed', 'summary', 'bugs_found'],
};

const REVIEW_SCHEMA = {
  type: 'object',
  properties: {
    findings: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          file: { type: 'string' },
          line: { type: 'integer' },
          severity: { type: 'string', enum: ['P1', 'P2', 'P3', 'P4'] },
          summary: { type: 'string' },
          scenario: { type: 'string' },
          fix: { type: 'string' },
        },
        required: ['file', 'line', 'severity', 'summary'],
      },
    },
    verified_clean: { type: 'array', items: { type: 'string' } },
    not_verified: { type: 'array', items: { type: 'string' } },
  },
  required: ['findings'],
};

const INTEGRATION_SCHEMA = {
  type: 'object',
  properties: {
    merged: {
      type: 'array',
      items: {
        type: 'object',
        properties: { branch: { type: 'string' }, commit: { type: 'string' } },
        required: ['branch', 'commit'],
      },
    },
    returned: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          branch: { type: 'string' },
          reason: { type: 'string' },
          owner: { type: 'string' },
        },
        required: ['branch', 'reason'],
      },
    },
    ci: {
      type: 'object',
      properties: { passed: { type: 'boolean' }, summary: { type: 'string' } },
      required: ['passed', 'summary'],
    },
  },
  required: ['merged', 'returned', 'ci'],
};

if (
  !args ||
  typeof args !== 'object' ||
  !Array.isArray(args.modules) ||
  args.modules.length === 0
) {
  throw new Error('build-phase needs args { phase: string, modules: [{ key, agent, prompt }] }');
}
const phaseName = args.phase || 'Unnamed phase';
const modules = args.modules.slice(0, MAX_MODULES);
if (args.modules.length > MAX_MODULES) {
  log(
    `build-phase: only the first ${MAX_MODULES} of ${args.modules.length} modules run in this call; run the rest in a second call`,
  );
}

// Build: one agent per module, each in its own worktree. The barrier is intended:
// test-engineer, security-reviewer and integrator all need every branch to exist.
phase('Build');
log(`${phaseName}: building ${modules.length} module(s) in parallel`);
const built = (
  await parallel(
    modules.map(
      (m, i) => () =>
        agent(
          `You are building module "${m.key}" for ${phaseName} of MiniLoyal.

Work only in the worktree you are started in. Commit on its branch with short imperative messages, no prefixes, no trailers. Never touch main.

Task:
${m.prompt}

Before you finish: run make lint and make test-short (or the package's npm scripts for frontend work) and make them pass. Report the branch name, the worktree path, every file you changed, the test command and result, anything you still need from other modules (interfaces, migrations, spec changes), and whether you are blocked. Set key to "${m.key}".`,
          {
            label: `build:${m.key}`,
            phase: 'Build',
            schema: MODULE_SCHEMA,
            isolation: 'worktree',
            agentType: m.agent || 'backend-go',
          },
        ).then((r) => (r ? { ...r, key: r.key || m.key, index: i } : null)),
    ),
  )
).filter(Boolean);

const branches = built.map((b) => b.branch).filter(Boolean);
const blocked = built.filter((b) => b.blocked);
if (blocked.length) {
  log(
    `build-phase: ${blocked.length} module(s) reported blocked: ${blocked.map((b) => b.key).join(', ')}`,
  );
}
if (built.length < modules.length) {
  log(
    `build-phase: ${modules.length - built.length} module agent(s) returned nothing; their branches are not included`,
  );
}

// Test: one test-engineer pass over all branches.
phase('Test');
const tests = branches.length
  ? await agent(
      `You are test-engineer for ${phaseName} of MiniLoyal.

The following branches were just built and are not merged yet:
${built.map((b) => `- ${b.branch} (module ${b.key}; changed: ${b.files_changed.slice(0, 20).join(', ')})`).join('\n')}

Check out each branch in turn (or a temporary merge of all of them in a scratch worktree), and add the tests the invariants demand: cross-tenant access, idempotency replay and payload mismatch, concurrency on balances and redemptions, LINE signature and token negatives where relevant. Commit test additions on the branch that owns the code. Run go test -race and the npm test scripts. Report invariants covered, bugs found with repro steps, and whether everything passes.`,
      { label: 'test-engineer', phase: 'Test', schema: TEST_SCHEMA, agentType: 'test-engineer' },
    )
  : null;
if (tests && !tests.passed) {
  log(`build-phase: test-engineer reports failures: ${tests.summary}`);
}

// Review: read-only security pass over every branch.
phase('Review');
const review = branches.length
  ? await agent(
      `You are security-reviewer for ${phaseName} of MiniLoyal. Read only; you cannot edit.

Review these unmerged branches against your checklist (tenant isolation, auth, idempotency, ledger integrity, secrets, crypto, LINE specifics, input handling, rate limits, PDPA, audit log):
${branches.map((b) => `- ${b}`).join('\n')}

Use git log and git diff main...<branch> through the files you can read, then follow every call into the code it touches. Return findings with file, line, severity P1 to P4, summary, scenario and fix, plus the checklist items verified clean and the ones you could not verify.`,
      {
        label: 'security-reviewer',
        phase: 'Review',
        schema: REVIEW_SCHEMA,
        agentType: 'security-reviewer',
      },
    )
  : { findings: [], verified_clean: [], not_verified: ['no branches were built'] };
const findings = (review && review.findings) || [];
const blocking = findings.filter((f) => f.severity === 'P1' || f.severity === 'P2');
log(`build-phase: ${findings.length} finding(s), ${blocking.length} blocking (P1/P2)`);

// Integrate: merge what is clean, return what is not.
phase('Integrate');
const integration = branches.length
  ? await agent(
      `You are integrator for ${phaseName} of MiniLoyal.

Branches ready for main:
${built.map((b) => `- ${b.branch} (module ${b.key}, tests passed: ${b.tests.passed}, blocked: ${b.blocked})`).join('\n')}

Test-engineer summary: ${tests ? tests.summary : 'not run'}
Bugs found by test-engineer: ${tests && tests.bugs_found.length ? JSON.stringify(tests.bugs_found) : 'none'}

Security findings (P1 and P2 block the branch they belong to until fixed):
${findings.length ? findings.map((f) => `- [${f.severity}] ${f.file}:${f.line} ${f.summary}`).join('\n') : '- none'}

Merge the clean branches onto main in dependency order (migrations, backend, frontend, SDK) with git merge --no-ff. Regenerate generated code instead of hand-merging it, run make ci before committing, never force-push, never reset main. Return a blocked branch to its owner with the reason instead of merging it. Remove merged worktrees and branches. Report merged branches with commit hashes, returned branches with reasons, and the CI result.`,
      {
        label: 'integrator',
        phase: 'Integrate',
        schema: INTEGRATION_SCHEMA,
        agentType: 'integrator',
      },
    )
  : { merged: [], returned: [], ci: { passed: false, summary: 'nothing to integrate' } };

return {
  phase: phaseName,
  modules: built,
  tests,
  findings,
  integration,
};
