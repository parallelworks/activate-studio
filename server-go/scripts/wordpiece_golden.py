"""Per code point behaviour of the tokenizers library's BertNormalizer and
BertPreTokenizer, for internal/embed/wordpiece's tables and tests.

  PYTHONPATH=indexer/.deps python3 -I scripts/wordpiece_golden.py TOKENIZER_JSON OUT_JSON

Writes, for every code point other than the surrogates and ASCII, the
normalized text of the single character and whether the pre-tokenizer
isolates it as punctuation, as {"<cp>": ["<normalized>", punct]} for the
code points where either is not the identity and not punctuation.
"""
import json
import sys

sys.path[:0] = [p for p in __import__('os').environ.get('PYTHONPATH', '').split(':') if p]
from tokenizers import Tokenizer  # noqa: E402

tok = Tokenizer.from_file(sys.argv[1])
norm = tok.normalizer
pre = tok.pre_tokenizer
out = {}
for cp in range(0x80, 0x110000):
    if 0xD800 <= cp <= 0xDFFF:
        continue
    ch = chr(cp)
    n = norm.normalize_str(ch)
    pieces = [p for p, _ in pre.pre_tokenize_str('a' + ch + 'a')]
    punct = pieces == ['a', ch, 'a']
    space = pieces == ['a', 'a']
    if n != ch or punct or space:
        out[cp] = [n, 1 if punct else 2 if space else 0]
json.dump(out, open(sys.argv[2], 'w'))
print(len(out))
