// Builds the main folder's index in a process of its own, as the Studio
// does after a restart on a folder: KB_ROOT, INDEX_BASE and GUFI_BIN come
// from the environment. Prints the finished job, and whether an index of
// EARLIER (another folder) is kept aside, as JSON.
const ix = await import('../../dist/indexing.js')
await ix.reconcileFolder()
const job = ix.startBuild()
for (;;) {
  const j = ix.getIndexJob(job.id)
  if (j.state !== 'queued' && j.state !== 'running') break
  await new Promise(r => setTimeout(r, 50))
}
console.log(JSON.stringify({
  job: ix.getIndexJob(job.id),
  earlier: process.env.EARLIER ? ix.earlierIndexOf(process.env.EARLIER) : null,
  status: ix.indexStatus(),
}))
process.exit(0)
