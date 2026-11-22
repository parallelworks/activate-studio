// States for the Settings, Indexing check: every kind of library and the
// states the page draws for them, with neutral names and paths only.
const caps = (fullText, vectors = false, index = true) => ({ index, fullText, vectors })
const T = '2026-11-04T16:46:06Z'

export const MAIN = { id: 'kb', label: 'notes', primary: true, writable: true, pinned: true, source: true, caps: caps(true, true), depth: 'meaning-full', meaning: 'full',
  indexRoot: '/Users/user/Library/Application Support/ACTIVATE Studio/index/gufi/notes', sourceRoot: '/Users/user/notes', readThrough: null }
const HERE = { id: 'papers', label: 'Papers', primary: false, writable: false, pinned: false, source: true, caps: caps(true, true), depth: 'meaning-documents',
  indexRoot: '/Users/user/Library/Application Support/ACTIVATE Studio/libraries/papers/index/papers', sourceRoot: '/Users/user/Documents/papers',
  build: { indexDir: '/Users/user/Library/Application Support/ACTIVATE Studio/libraries/papers/index', managed: true, state: 'ready', error: null, builtAt: T, files: 3120, dirs: 210, progress: null } }
const HERE_BUILDING = { ...HERE, id: 'mail', label: 'Mail', sourceRoot: '/Users/user/Library/Mail', indexRoot: '/Users/user/indexes/mail/Mail',
  build: { indexDir: '/Users/user/indexes/mail', managed: false, state: 'building', error: null, builtAt: null, files: null, dirs: null, progress: { phase: 'Indexing folders', done: 120, total: 0, current: '120 folders' } } }
const HERE_FAILED = { ...HERE, id: 'scans', label: 'Scans', sourceRoot: '/Users/user/scans', indexRoot: '/Users/user/indexes/scans/scans',
  build: { indexDir: '/Users/user/indexes/scans', managed: true, state: 'failed', error: 'gufi_dir2index exited with status 1', builtAt: null, files: null, dirs: null, progress: null } }
const HERE_GONE = { ...HERE, id: 'old', label: 'Old drafts', caps: caps(false, false, false), sourceRoot: '/Users/user/drafts', indexRoot: '/Users/user/indexes/drafts/drafts',
  build: { indexDir: '/Users/user/indexes/drafts', managed: true, state: 'ready', error: null, builtAt: T, files: 40, dirs: 3, progress: null } }
const GUFI_HERE = { id: 'site', label: 'Site index', primary: false, writable: false, pinned: false, source: false, caps: caps(true), indexRoot: '/data/site-index', sourceRoot: null, readThrough: null }
const PINNED = { id: 'scratch', label: 'Scratch', primary: false, writable: false, pinned: true, source: true, caps: caps(false, false, false), indexRoot: '/data/scratch-index', sourceRoot: '/data/scratch' }
const COPY = { id: 'upload', label: 'upload', primary: false, writable: false, pinned: false, source: true, caps: caps(true), depth: 'text',
  indexRoot: '/Users/user/Library/Application Support/ACTIVATE Studio/libraries/upload/index/files', sourceRoot: '/Users/user/Library/Application Support/ACTIVATE Studio/libraries/upload/files',
  build: { indexDir: '/x/upload/index', managed: true, state: 'ready', error: null, builtAt: '2026-11-16T12:00:00Z', files: 3, dirs: 1, progress: null },
  copyOf: { location: 'saved:drop', path: 'upload', label: 'Drop', every: 15, checkedAt: '2026-11-16T12:30:00Z', changedAt: '2026-11-16T12:00:00Z', transfer: 'abc', files: 3, deleted: 0, error: null, looking: false, running: null } }
const BUCKET = { id: 'research', label: 'research', primary: false, writable: false, pinned: false, source: false, caps: caps(true), fromBucket: true,
  indexRoot: '/Users/user/Library/Application Support/ACTIVATE Studio/libraries/research/index/files', sourceRoot: null,
  build: { indexDir: '/x/research/index', managed: true, state: 'ready', error: null, builtAt: '2026-11-19T12:00:00Z', files: 2_500_000, dirs: 4000, progress: null },
  bucket: { location: 'rclone:store', path: 'research', label: 'store', every: 1440, listedAt: '2026-11-19T12:00:00Z', objects: 2_500_000, bytes: 3 * 1024 ** 4, folders: 4000, skipped: 2,
    requests: 2500, provider: 'AWS', seconds: 95, error: null, listing: false, text: true, reading: false,
    textPass: { at: '2026-11-19T13:00:00Z', read: { text: 1200, failed: 3 }, bytes: 900 * 1024 ** 2, requests: 1203, seconds: 600, files: 1200, rate: 1, missing: [], error: null } } }
const BUCKET_READING = { ...BUCKET, id: 'archive', label: 'archive', bucket: { ...BUCKET.bucket, path: 'archive', reading: true, error: 'listing timed out' },
  build: { ...BUCKET.build, progress: { phase: 'Reading text', done: 40, total: 1200, current: '40 of 1,200 objects' } } }

const REMOTE_BUILT = { id: 'u-kb', label: 'KNOWLEDGE_BASE', transport: 'pw', site: 'cluster-a', destination: null, port: null, sourceDir: '/shared/KNOWLEDGE_BASE', sourceRoot: '/shared/KNOWLEDGE_BASE',
  indexRoot: '/home/user/.activate-studio/indexes/KNOWLEDGE_BASE-22300623/KNOWLEDGE_BASE', indexDir: '/home/user/.activate-studio/indexes/KNOWLEDGE_BASE-22300623',
  state: 'ready', run: null, error: null, builtAt: T, directories: 4653, files: 36337, scheduler: null, fullText: true, vectors: true, depth: 'meaning-full', builtDepth: 'meaning-full',
  built: true, mine: true, readThrough: null }
const REMOTE_SSH_BUILDING = { ...REMOTE_BUILT, id: 'u-box-data', label: 'data on box', transport: 'ssh', site: 'user@box.example.org', destination: 'user@box.example.org', reached: 'user@box.example.org', port: 2222,
  sourceDir: '/home/user/data', sourceRoot: '/home/user/data', indexRoot: null, indexDir: '/home/user/.activate-studio/indexes/data-1a2b3c4d', state: 'building', directories: null, files: null, builtAt: null,
  depth: 'meaning-documents', builtDepth: 'text', buildingDepth: 'text', progress: { phase: 'Extracting text', done: 0, total: 0, current: null }, mine: false }
const REMOTE_GAPS = { ...REMOTE_BUILT, id: 'u-proj', label: 'projects', sourceDir: '/work/projects', sourceRoot: '/work/projects', indexRoot: '/home/user/.activate-studio/indexes/projects-9f/projects',
  depth: 'meaning-documents', builtDepth: 'text', gaps: ['12 files could not be read: the reader there timed out'], readThrough: { destination: 'user@xfer.example.org', port: null, sshOptions: null, root: '/work/projects' } }
const SSH_INDEX = { id: 'u-box-index', label: 'index on box', transport: 'ssh', site: 'user@box.example.org', destination: 'user@box.example.org', port: 2222, sshOptions: '-o ProxyJump=user@jump.example.org',
  sourceDir: '', sourceRoot: null, indexRoot: '/data/index', state: 'ready', run: null, error: 'ssh: connect to host box.example.org port 2222: Connection timed out', builtAt: '2026-10-01T00:00:00Z', directories: 12, files: 40, scheduler: null }
const PW_INDEX = { id: 'u-c1-proj', label: 'proj index', transport: 'pw', site: 'cluster-b', existing: true, sourceDir: '', sourceRoot: '/proj/files', indexRoot: '/proj/gufi-index',
  state: 'ready', run: null, error: null, builtAt: '2026-10-02T00:00:00Z', directories: 900, files: 12000, scheduler: null }
const WORKFLOW = { id: 'u-c1-workdir', label: 'cluster-a $WORKDIR', transport: 'pw', site: 'cluster-a', sourceDir: '$WORKDIR', sourceRoot: null, indexRoot: null,
  state: 'building', run: { slug: 'run-17', mode: 'build', startedAt: '2026-10-01T00:00:00Z' }, error: null, builtAt: null, directories: null, files: null, scheduler: null }
const SPACE = { ...REMOTE_BUILT, id: 'u-space', label: 'My library', sourceDir: '/home/user/studio', sourceRoot: '/home/user/studio', indexRoot: '/home/user/.activate-studio/indexes/space/studio',
  space: { account: 'user', acl: true, fs: 'xfs' }, directories: 20, files: 310, depth: 'text', builtDepth: 'text' }
const SHARED = { ...REMOTE_BUILT, id: 'u-shared', label: 'results from alex', sourceDir: '/home/alex/results', sourceRoot: '/home/alex/results', indexRoot: '/home/alex/.activate-studio/indexes/results/results',
  shared: { from: 'alex', access: 'read', share: 's1' }, mine: false, directories: 8, files: 77, depth: 'text', builtDepth: 'text' }

const pub = (l, extra = {}) => ({ ...l, indexRoot: undefined, sourceRoot: undefined, ...extra })
const userPub = l => ({ id: l.id, label: l.label, primary: false, writable: !!(l.mine || l.space || l.shared?.access === 'write'), pinned: false, source: true,
  caps: { index: true, fullText: !!l.fullText, vectors: !!l.vectors }, site: l.site, sharedFrom: l.shared?.from, space: !!l.space, mine: !!l.mine })

const job = state => ({ id: 7, kind: 'build', dir: '', label: 'Updating the index', state, startedAt: '2026-10-23T13:52:46Z', runningAt: '2026-10-23T13:52:46Z',
  finishedAt: state === 'running' ? null : '2026-10-23T13:53:42Z', ms: state === 'running' ? null : 56000, error: state === 'failed' ? 'gufi_dir2index exited with status 2' : null, threads: 16,
  phases: [
    { id: 'names', label: 'Names and folders', state: 'done', done: 3, total: 3, unit: 'folders', step: null, ms: 4500, note: null },
    { id: 'documents', label: 'Documents (PDF, Word, PowerPoint, Excel)', state: 'done', done: 2, total: 2, unit: 'documents', step: null, ms: 875, note: '2 converted' },
    { id: 'text', label: 'Text, images and full-text search', state: state === 'running' ? 'running' : 'done', done: state === 'running' ? 4 : 9, total: 9, unit: 'items', step: 'extract', ms: state === 'running' ? null : 25000, note: null },
    { id: 'meaning', label: 'Search by meaning', state: state === 'running' ? 'pending' : 'done', done: 0, total: state === 'running' ? 0 : 30, unit: 'passages', step: null, ms: state === 'running' ? null : 18000, note: null },
  ], searchableAt: null, warnings: [], warningCount: 0 })

/** One scenario: what each endpoint answers. */
function scenario({ libs, user, standalone = true, indexed = true, build = 'done', primaryExists = true, earlier = false, signIn = false, readOnly = false }) {
  const main = libs.find(l => l.primary)
  if (main && readOnly) main.readOnlyWhy = 'The main library is read only for you; its administrators change it.'
  const running = build === 'running'
  const last = build === 'done' || build === 'failed' || build === 'cancelled' ? job(build) : null
  return {
    signIn,
    '/api/libraries': { canBuild: standalone, libraries: libs },
    '/api/user-libraries': { libraries: user, available: true, ssh: standalone, pw: true, pwOffered: true, pwWhy: null, workflow: true },
    '/api/user-libraries/space': { available: signIn, why: '', cluster: signIn ? 'cluster-a' : '', suggested: '', spaces: [] },
    '/api/setup/status': standalone ? { firstRun: false, standalone: true, canRestart: true, launcher: true,
      primary: { path: main?.sourceRoot, exists: primaryExists, writable: true, indexed, files: indexed ? 126618 : undefined, earlierIndex: earlier ? { builtAt: '2026-09-01T00:00:00Z' } : undefined },
      gufi: { found: true, bin: '/opt/gufi/bin', semantic: true }, job: null } : { firstRun: false, standalone: false, canRestart: false },
    '/api/index/status': { running, job: running ? job('running') : null, queued: [], last, lastBuild: last, runs: last ? [last] : [], indexed, incomplete: build === 'cancelled',
      threads: 16, lastSweepAt: null, lastSweepMs: null, lastChanges: [], lastError: null, sweepIntervalSec: 300, log: { path: '/Users/user/Library/Logs/ACTIVATE Studio/indexing.log', bytes: 10, reveal: true } },
    configLibraries: [...libs.map(l => pub(l)), ...user.map(userPub)],
  }
}

export const SCENARIOS = {
  // The screenshot's two libraries.
  pair: () => scenario({ libs: [structuredClone(MAIN)], user: [structuredClone(REMOTE_BUILT)] }),
  // Every kind, a computer of its own.
  single: () => scenario({ libs: [MAIN, HERE, GUFI_HERE, COPY, BUCKET].map(x => structuredClone(x)), user: [REMOTE_BUILT, REMOTE_GAPS, SSH_INDEX, PW_INDEX, WORKFLOW].map(x => structuredClone(x)) }),
  // Builds running, failures, a missing index, a depth waiting for a rebuild.
  states: () => scenario({ libs: [MAIN, HERE_BUILDING, HERE_FAILED, HERE_GONE, BUCKET_READING].map(x => structuredClone(x)), user: [REMOTE_SSH_BUILDING, WORKFLOW].map(x => structuredClone(x)), build: 'running' }),
  // The main folder not indexed yet, an earlier index kept, the last build failed.
  notindexed: () => scenario({ libs: [structuredClone(MAIN)], user: [], indexed: false, earlier: true, build: 'failed' }),
  // Sign-in on, as an administrator: the deployment builds the main index; a pinned library.
  admin: () => scenario({ libs: [MAIN, PINNED, GUFI_HERE].map(x => structuredClone(x)), user: [SPACE, SHARED, REMOTE_BUILT].map(x => structuredClone(x)), standalone: false, signIn: true }),
  // Sign-in on, as anyone else: the main library read only, its depth set by the administrators.
  user: () => scenario({ libs: [MAIN, PINNED].map(x => structuredClone(x)), user: [SPACE, SHARED].map(x => structuredClone(x)), standalone: false, signIn: true, readOnly: true }),
}
