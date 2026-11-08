# Changelog

## [0.6.0](https://github.com/giraffesyo/pdf/compare/v0.5.0...v0.6.0) (2026-08-21)


### Features

* report a page's image count, and take a predicate for OCR selection ([#33](https://github.com/giraffesyo/pdf/issues/33)) ([7a48b44](https://github.com/giraffesyo/pdf/commit/7a48b444fbf33c870e236da759d1722ef646bbe2))

## [0.5.0](https://github.com/giraffesyo/pdf/compare/v0.4.0...v0.5.0) (2026-08-21)


### ⚠ BREAKING CHANGES

* report page images, decode CCITT/JBIG2, and hand OCR the page's images ([#30](https://github.com/giraffesyo/pdf/issues/30))

### Features

* report page images, decode CCITT/JBIG2, and hand OCR the page's images ([#30](https://github.com/giraffesyo/pdf/issues/30)) ([77aaa5d](https://github.com/giraffesyo/pdf/commit/77aaa5d3029604078d8436c24210bd3a078df37f))


### Bug Fixes

* bound the object numbers a cross-reference rebuild trusts ([#32](https://github.com/giraffesyo/pdf/issues/32)) ([c5515a7](https://github.com/giraffesyo/pdf/commit/c5515a73561457fbbed98bab5dcdc3ba20f0da07))

## [0.4.0](https://github.com/giraffesyo/pdf/compare/v0.3.0...v0.4.0) (2026-08-21)


### ⚠ BREAKING CHANGES

* derive glyph baseline and quad instead of storing them ([#17](https://github.com/giraffesyo/pdf/issues/17))

### Performance

* cache fonts per document and cut layout and lexer allocation ([#14](https://github.com/giraffesyo/pdf/issues/14)) ([26f36cc](https://github.com/giraffesyo/pdf/commit/26f36cc1a79a44ee1a35bcd5c7a814036d9d2f11))
* derive glyph baseline and quad instead of storing them ([#17](https://github.com/giraffesyo/pdf/issues/17)) ([a0a78e9](https://github.com/giraffesyo/pdf/commit/a0a78e945442ffb135b0efe253d5a6f3545f968d))
* extract and lay out pages concurrently ([#27](https://github.com/giraffesyo/pdf/issues/27)) ([a1b6850](https://github.com/giraffesyo/pdf/commit/a1b6850734e72c654a22914fe32d38cace740eac))
* parse embedded font programs on first use and reuse the object parse window ([#20](https://github.com/giraffesyo/pdf/issues/20)) ([ffe0d4a](https://github.com/giraffesyo/pdf/commit/ffe0d4aac878b4a1b4961ba5e901e9412c9a9cb6))
* reuse page-walk buffers across a document and size the xref table on demand ([#24](https://github.com/giraffesyo/pdf/issues/24)) ([75019bd](https://github.com/giraffesyo/pdf/commit/75019bdd1af3d93e10fe44d41e91965a7d025f2c))
* skip empty layout buckets, pool Flate decompressors, grow stream buffers gently ([#22](https://github.com/giraffesyo/pdf/issues/22)) ([207cc88](https://github.com/giraffesyo/pdf/commit/207cc887c709af2a0d672f4f78ca117e7982bda6))
* skip the line search for same-baseline glyphs, the sort for ordered lines, and repeated simple-font decoding ([#23](https://github.com/giraffesyo/pdf/issues/23)) ([fa9719a](https://github.com/giraffesyo/pdf/commit/fa9719ae8571975373b32b38aab9add57499bba0))

## [0.3.0](https://github.com/giraffesyo/pdf/compare/v0.2.1...v0.3.0) (2026-08-21)


### Features

* complete PDF extraction capabilities ([#11](https://github.com/giraffesyo/pdf/issues/11)) ([1c8295e](https://github.com/giraffesyo/pdf/commit/1c8295e874b9f36bed186235aa18d0f76a28f929))


### Bug Fixes

* fold ligature presentation forms to letter sequences ([#10](https://github.com/giraffesyo/pdf/issues/10)) ([44a3964](https://github.com/giraffesyo/pdf/commit/44a396425d41cd564ab084272e041e2001f4a170))
