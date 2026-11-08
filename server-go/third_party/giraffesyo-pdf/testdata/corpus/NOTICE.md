# Regression corpus

Real PDFs produced by real generators, used by `TestCorpusGolden` (which
pins `Document.Text` in `golden/`) and `BenchmarkCorpus`. Each file is
redistributable under the license noted; none is original to this project.

| File | Pages | Embedded fonts | Source | License |
|---|---:|---|---|---|
| `runc-security-audit.pdf` | 16 | TrueType (`FontFile2`) | [opencontainers/runc](https://github.com/opencontainers/runc) `docs/Security-Audit.pdf` | Apache-2.0 |
| `alice-fr.pdf` | 7 | TrueType, CFF (`FontFile3/Type1C`) | [gohugoio/hugo-learn-theme](https://github.com/matcornic/hugo-theme-learn) exampleSite attachments, `Carroll_AliceAuPaysDesMerveilles.pdf` (public-domain text) | MIT |
| `adivorciar.pdf` | 4 | TrueType | hugo-learn-theme exampleSite attachments, `adivorciarsetoca00cape.pdf` (1932 play, public domain; OCR text layer) | MIT |
| `markitdown-test.pdf` | 1 | Type1 (`FontFile`) ×4 | [microsoft/markitdown](https://github.com/microsoft/markitdown) `packages/markitdown/tests/test_files/test.pdf` | MIT, © Microsoft Corporation |
| `downmark-synthetic.pdf` | 1 | none (Form XObject + ToUnicode) | [giraffesyo/downmark](https://github.com/giraffesyo/downmark) `testdata/synthetic.pdf` | MIT |

Files that cannot be redistributed (for example arXiv papers) can be
benchmarked locally by pointing `PDF_CORPUS_DIR` at a directory of PDFs.

To accept an intentional output change:

    go test -run TestCorpusGolden -update
