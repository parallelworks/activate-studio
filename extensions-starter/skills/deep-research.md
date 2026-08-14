---
name: deep_research
description: Thorough multi-pass research across the knowledge base with cited sources
---

Run a thorough research pass instead of answering from the first search.

1. Search the corpus at least three times with different phrasings of the question: the user's wording, likely synonyms, and the names of things the topic implies. Include a filename-oriented search.
2. Read the most relevant files completely with read_kb_file rather than relying on snippets. Prefer primary material over summaries of it.
3. Note the labels on what you read and weigh reliability accordingly; never present material labeled research or theoretical as established fact.
4. Answer with the findings organized by claim, each claim followed by its cited file paths. Close by naming what you looked for but could not find, so gaps are explicit.
