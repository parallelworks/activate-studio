---
name: corpus_health
description: Report the state of the knowledge base, size, growth, file mix, labels
---

Build a corpus health report from the index rather than from memory.

1. Use query_corpus for totals, the by-extension breakdown, the largest files and directories, and what changed recently.
2. Use get_labels for the labeling vocabulary and its coverage.
3. Report: overall size and file count, the file-type mix, where the bulk of the data sits, what changed in the last month, how much of the corpus carries labels, and anything that looks anomalous (very large files, stale areas, unlabeled regions). Cite directory paths.
